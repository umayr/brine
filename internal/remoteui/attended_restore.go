package remoteui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/umayr/brine/internal/projectreview"
	"github.com/umayr/brine/internal/snapshothistory"
)

var errRestoreReview = errors.New("cannot prepare a new deployment review from archived configuration")

type restoreReader interface {
	Open(context.Context, snapshothistory.OpenRequest) (snapshothistory.Record, error)
	ReadEvidence(context.Context, snapshothistory.OpenRequest) (snapshothistory.Evidence, error)
}

// authorizeRestore checks backend availability at the point of use. A retained
// review is not authority, even if it was issued by a previously available gate.
func (h *attendedHistory) authorizeRestore(ctx context.Context, session string, current projectreview.Current, id snapshothistory.ObjectID) bool {
	if h == nil || ctx == nil || ctx.Err() != nil {
		return false
	}
	gate := h.restoreGate
	return gate != nil && gate(ctx, session, current, id) == nil && ctx.Err() == nil
}

type restoreBindingResolver interface {
	ResolveAttendedBinding(context.Context, snapshothistory.AttendedBindingSelector, snapshothistory.AttendedBindingAuthorizer) (snapshothistory.OpenRequest, error)
}

// prepareRestoreReviewFromLocator is an unmounted server entry point. Only the
// locator and target/stack selectors may originate in a request. An authenticated
// caller supplies the session; the target lease and qualified connection derive
// all current authority. A stale enrollment or changed grant revision cannot open.
func (h *Deployment) prepareRestoreReviewFromLocator(ctx context.Context, session, host, stack string, locator snapshothistory.ObjectID) (projectreview.Preview, error) {
	if ctx == nil || ctx.Err() != nil || h.history == nil || h.history.restoreGate == nil || h.history.operator != nil || h.registry == nil || session == "" || stack == "" {
		return projectreview.Preview{}, errRestoreReview
	}
	resolver, ok := h.history.archive.(restoreBindingResolver)
	if !ok {
		return projectreview.Preview{}, errRestoreReview
	}
	record, incarnation, lease, err := h.registry.AcquireHistoryRead(ctx, host)
	if err != nil {
		return projectreview.Preview{}, errRestoreReview
	}
	defer lease.Close()
	if record.HTTPSAddress == "" {
		return projectreview.Preview{}, errRestoreReview
	}
	target := targetDigest(record)
	current := projectreview.Current{Stack: stack, Target: projectreview.Target{Kind: projectreview.HTTPS, Identity: target, Socket: record.HTTPSAddress, Endpoint: host}}
	if !h.history.authorizeRestore(ctx, session, current, locator) {
		return projectreview.Preview{}, errRestoreReview
	}
	c, err := h.history.connection(ctx, record, target)
	if err != nil {
		return projectreview.Preview{}, errRestoreReview
	}
	defer c.client.Close()
	// The explicit scope format must be shared with a future qualified writer.
	// Never try a legacy scope when this active enrollment cannot resolve.
	scope := hex.EncodeToString(incarnation[:])
	if c.binding.scope != scope {
		return projectreview.Preview{}, errRestoreReview
	}
	q := snapshothistory.AttendedBindingSelector{Locator: locator, Scope: scope, Target: target, AuthorityRevision: c.binding.revision, Stack: stack}
	prior, err := resolver.ResolveAttendedBinding(ctx, q, func(ctx context.Context, requested snapshothistory.AttendedBindingSelector) error {
		if requested != q || !h.history.authorizeRestore(ctx, session, current, locator) {
			return errRestoreReview
		}
		return nil
	})
	if err != nil {
		return projectreview.Preview{}, errRestoreReview
	}
	return h.prepareRestoreReview(ctx, session, current, c.binding, prior)
}

// prepareRestoreReview is not a route. A future caller must supply an authenticated
// interactive session and own the exact active target read lease. The qualified
// gate must authorize the actor to open this exact historical object, and attest
// that no conflicting current operation blocks a new review. Neither a successful
// historical receipt nor the deployment grant is that local authorization.
func (h *Deployment) prepareRestoreReview(ctx context.Context, session string, current projectreview.Current, binding attendedBinding, prior snapshothistory.OpenRequest) (projectreview.Preview, error) {
	if ctx == nil || ctx.Err() != nil || h.history == nil || h.reviews == nil || session == "" || current.Target.Kind != projectreview.HTTPS || !binding.distinct || binding.grants&attendedGrants != attendedGrants {
		return projectreview.Preview{}, errRestoreReview
	}
	reader, ok := h.history.archive.(restoreReader)
	a := prior.Intent.Attended
	if !ok || a == nil || prior.ObjectID == (snapshothistory.ObjectID{}) || a.Stack != current.Stack || a.Target != current.Target.Identity || a.Target != binding.target || a.AuthorityRevision != binding.revision || string(prior.Intent.ApprovedTargetScope) != binding.scope || !attendedHex(string(prior.Intent.AttemptID), 16) || !attendedHex(string(prior.Intent.ProviderRequestID), 16) || !attendedHex(string(prior.Intent.OriginalJournal), 16) || !attendedHex(a.AuthorityRevision, 32) {
		return projectreview.Preview{}, errRestoreReview
	}
	if !h.history.authorizeRestore(ctx, session, current, prior.ObjectID) {
		return projectreview.Preview{}, errRestoreReview
	}
	evidence, err := reader.ReadEvidence(ctx, prior)
	if err != nil || evidence.State != snapshothistory.VerifiedReceipt || evidence.Receipt == nil || evidence.Receipt.State != "succeeded" || evidence.Receipt.Effects != "complete" || evidence.Receipt.ContractVersion != binding.contractVersion {
		return projectreview.Preview{}, errRestoreReview
	}
	saved, err := reader.Open(ctx, prior)
	if err != nil || saved.ObjectID != prior.ObjectID || saved.ManifestDigest != prior.ManifestDigest || ctx.Err() != nil {
		return projectreview.Preview{}, errRestoreReview
	}
	p := &projectreview.PriorDeployment{ObjectID: prior.ObjectID, ScopeDigest: sha256.Sum256(prior.Intent.ApprovedTargetScope)}
	_, _ = hex.Decode(p.AttemptID[:], prior.Intent.AttemptID)
	_, _ = hex.Decode(p.RequestID[:], prior.Intent.ProviderRequestID)
	_, _ = hex.Decode(p.Journal[:], prior.Intent.OriginalJournal)
	_, _ = hex.Decode(p.AuthorityRevision[:], []byte(a.AuthorityRevision))
	if !h.history.authorizeRestore(ctx, session, current, prior.ObjectID) {
		return projectreview.Preview{}, errRestoreReview
	}
	return h.reviews.Create(ctx, session, projectreview.Intent{Snapshot: saved.Snapshot, Current: current, PriorDeployment: p})
}

func restoreBindingMatches(prior *projectreview.PriorDeployment, binding attendedBinding, journal string) bool {
	return prior != nil && prior.ScopeDigest == sha256.Sum256([]byte(binding.scope)) &&
		binding.revision == hex.EncodeToString(prior.AuthorityRevision[:]) && (journal == "" || journal == hex.EncodeToString(prior.Journal[:]))
}

func freshRestoreIDs(prior *projectreview.PriorDeployment, attempt, request string) bool {
	if prior == nil {
		return true
	}
	oldAttempt, oldRequest := hex.EncodeToString(prior.AttemptID[:]), hex.EncodeToString(prior.RequestID[:])
	return attempt != oldAttempt && attempt != oldRequest && request != oldAttempt && request != oldRequest && request != attempt
}
