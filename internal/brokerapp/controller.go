package brokerapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"github.com/umayr/brine/internal/brokerwire"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/secretbroker"
	"sync"
	"time"
)

type TargetInfo struct{ Journal, TargetDigest, EndpointDigest string }
type Outcome struct{ RequestID, Stack, State, Effects, OperationID string }
type Target interface {
	Info(context.Context) (TargetInfo, error)
	Requirements(context.Context, string, string, string, string) (brokerwire.Requirements, error)
	Prepare(context.Context, string, string, brokerwire.Envelope) (brokerwire.Review, error)
	Review(context.Context, string, string, brokerwire.Reference) (*dill.BrokerReview, string, error)
	Apply(context.Context, string, string, string, string, string, brokerwire.Reference) (Outcome, error)
	Status(context.Context, string, string, brokerwire.Reference) (Outcome, error)
}
type Guard func(context.Context, secretbroker.ReviewIdentity) (func(), error)
type PlanReview struct {
	Requirements             brokerwire.Requirements
	Retirement               *brokerwire.Retirement
	ID                       string
	Identity                 secretbroker.ReviewIdentity
	Reference                brokerwire.Reference
	StageID, JournalID, Mode string
	ExpiresAt                time.Time
	Revisions                []secretbroker.Revision
	Native                   *dill.BrokerReview
}
type Controller struct {
	service  *Service
	guard    Guard
	mu       sync.Mutex
	pending  map[string]PlanReview
	reserved int
	now      func() time.Time
}

func NewController(s *Service, g Guard) (*Controller, error) {
	if !s.Enabled() || g == nil {
		return nil, ErrUnavailable
	}
	return &Controller{service: s, guard: g, pending: map[string]PlanReview{}, now: time.Now}, nil
}
func copyReview(r PlanReview) PlanReview {
	raw, _ := json.Marshal(r)
	var copy PlanReview
	_ = json.Unmarshal(raw, &copy)
	return copy
}
func (c *Controller) purge() {
	for id, r := range c.pending {
		if !c.now().Before(r.ExpiresAt) {
			delete(c.pending, id)
		}
	}
}

func (c *Controller) settledDeployments(scope secretbroker.Scope) ([]secretbroker.DeploymentRecord, error) {
	records, err := c.service.State.Deployments(scope)
	if err != nil {
		return nil, ErrUnavailable
	}
	for _, r := range records {
		if len(r.Events) == 0 {
			return nil, ErrUnavailable
		}
		switch r.Events[len(r.Events)-1].State {
		case "pending", "running", "unknown":
			return nil, ErrUnavailable
		}
	}
	return records, nil
}

// Prepare requires an independently captured/verified source digest and stage.
// guard owns actual session/target/source authorization and the stack lock.
func (c *Controller) Prepare(ctx context.Context, i secretbroker.ReviewIdentity, stage, manifest string, target Target) (PlanReview, error) {
	if c == nil || ctx == nil || target == nil || !brokerwire.ValidHandle(stage) || i.SourceDigest != "sha256:"+manifest {
		return PlanReview{}, ErrUnavailable
	}
	release, err := c.guard(ctx, i)
	if err != nil {
		return PlanReview{}, ErrUnavailable
	}
	defer release()
	records, err := c.settledDeployments(i.Scope)
	if err != nil {
		return PlanReview{}, err
	}
	c.mu.Lock()
	c.purge()
	if len(c.pending)+c.reserved >= 32 {
		c.mu.Unlock()
		return PlanReview{}, ErrUnavailable
	}
	c.reserved++
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.reserved--; c.mu.Unlock() }()
	info, err := target.Info(ctx)
	if err != nil || !brokerwire.ValidHandle(info.Journal) || info.TargetDigest != i.TargetDigest {
		return PlanReview{}, ErrUnavailable
	}
	req, err := target.Requirements(ctx, info.Journal, stage, manifest, i.Scope.StackName)
	if err != nil || !req.Valid() || req.Stack != i.Scope.StackName || (req.Engine != "docker" && req.Engine != "podman") {
		return PlanReview{}, ErrUnavailable
	}
	if engine, ok := target.(interface{ Engine() string }); ok && req.Engine != engine.Engine() {
		return PlanReview{}, ErrUnavailable
	}
	retiring := len(req.Names) == 0 && len(req.Grants) == 0
	var retirement *brokerwire.Retirement
	var prepared secretbroker.Preparation
	if retiring {
		id, e := randomID()
		if e != nil {
			return PlanReview{}, ErrUnavailable
		}
		prepared = secretbroker.Preparation{ID: id, ExpiresAt: c.now().Add(5 * time.Minute)}
	} else {
		prepared, err = c.service.Manager.Prepare(ctx, i, req.Names)
	}
	if err != nil {
		return PlanReview{}, ErrUnavailable
	}
	scope := brokerwire.Scope{Registration: i.Scope.RegistrationID, Stack: i.Scope.StackName, Engine: req.Engine, TargetDigest: i.TargetDigest, SourceDigest: i.SourceDigest, ConfigDigest: req.ConfigDigest, EndpointDigest: info.EndpointDigest, BindingRevision: uint64(i.BindingRevision), PolicyRevision: i.PolicyRevision, Generation: prepared.ID}
	var delivery brokerwire.Review
	if retiring {
		request, e := c.retirementRequest(i, scope, records)
		if e != nil {
			return PlanReview{}, ErrUnavailable
		}
		retirement = &request
		transport, ok := target.(interface {
			Retire(context.Context, string, string, brokerwire.Retirement) (brokerwire.Review, error)
		})
		if !ok {
			return PlanReview{}, ErrUnavailable
		}
		err = c.service.Manager.WithCurrent(ctx, i, func() error { var e error; delivery, e = transport.Retire(ctx, info.Journal, stage, request); return e })
	} else {
		err = c.service.Manager.Dispatch(ctx, prepared.ID, i, func(_ secretbroker.Preparation, d *secretbroker.Delivery) error {
			envelope, err := brokerwire.FromValues(scope, d)
			if err != nil {
				return err
			}
			defer envelope.Close()
			delivery, err = target.Prepare(ctx, info.Journal, stage, envelope)
			return err
		})
	}
	if err != nil || delivery.Scope != scope || !brokerwire.ValidHandle(delivery.Handle) || delivery.Active || !c.now().Before(delivery.ExpiresAt) {
		return PlanReview{}, ErrUnavailable
	}
	ref := brokerwire.Reference{Scope: scope, Handle: delivery.Handle}
	native, mode, err := target.Review(ctx, info.Journal, stage, ref)
	if err != nil || native == nil || native.Plan == nil || native.Preflight == nil || native.Preflight.SelectionDigest == nil || native.Reference != ref || (mode != "create" && mode != "update") {
		return PlanReview{}, ErrUnavailable
	}
	expires := prepared.ExpiresAt
	if delivery.ExpiresAt.Before(expires) {
		expires = delivery.ExpiresAt
	}
	review := PlanReview{ID: prepared.ID, Identity: i, Reference: ref, StageID: stage, JournalID: info.Journal, Mode: mode, ExpiresAt: expires, Revisions: prepared.Revisions, Native: native, Requirements: req, Retirement: retirement}
	// Confirm bindings/policy again before publishing a review after transport.
	if c.service.Manager.WithCurrent(ctx, i, func() error { return nil }) != nil {
		return PlanReview{}, ErrUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending[review.ID] = copyReview(review)
	return copyReview(review), nil
}
func randomID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", ErrUnavailable
	}
	return hex.EncodeToString(id[:]), nil
}
func (c *Controller) Submit(ctx context.Context, id string, i secretbroker.ReviewIdentity, target Target) (secretbroker.DeploymentRecord, error) {
	var record secretbroker.DeploymentRecord
	if c == nil || ctx == nil || target == nil {
		return record, ErrUnavailable
	}
	release, err := c.guard(ctx, i)
	if err != nil {
		return record, ErrUnavailable
	}
	defer release()
	c.mu.Lock()
	c.purge()
	review, ok := c.pending[id]
	if !ok || review.Identity != i {
		c.mu.Unlock()
		return record, ErrUnavailable
	}
	delete(c.pending, id)
	c.mu.Unlock()
	info, err := target.Info(ctx)
	if err != nil || info.Journal != review.JournalID || info.TargetDigest != i.TargetDigest || info.EndpointDigest != review.Reference.Scope.EndpointDigest {
		return record, ErrUnavailable
	}
	request, err := randomID()
	if err != nil {
		return record, err
	}
	if _, err := c.settledDeployments(i.Scope); err != nil {
		return record, err
	}
	intent := secretbroker.DeploymentIntent{ID: request, Identity: i, Reference: review.Reference, StageID: review.StageID, JournalID: review.JournalID, SelectionDigest: *review.Native.Preflight.SelectionDigest, Mode: review.Mode, Revisions: review.Revisions, Retirement: review.Retirement}
	if err := c.service.State.BeginDeployment(intent); err != nil {
		return record, ErrUnavailable
	}
	record.Intent = intent
	record.At = c.now()
	sent := false
	var outcome Outcome
	var sendErr error
	err = c.service.Manager.WithCurrent(ctx, i, func() error {
		sent = true
		outcome, sendErr = target.Apply(ctx, review.JournalID, review.StageID, request, intent.SelectionDigest, review.Mode, review.Reference)
		return nil
	})
	event := secretbroker.DeploymentEvent{Sequence: 1, State: "unknown", Effects: "unknown"}
	if !sent {
		event.State = "not-admitted"
		event.Effects = "none"
	} else if err == nil && sendErr == nil && outcome.RequestID == request && outcome.Stack == i.Scope.StackName {
		candidate := secretbroker.DeploymentEvent{Sequence: 1, State: outcome.State, Effects: outcome.Effects, OperationID: outcome.OperationID}
		if candidate.Valid() && candidate.State != "pending" && candidate.State != "not-admitted" {
			event = candidate
		}
	}
	if c.service.State.AppendDeploymentEvent(i.Scope, request, 0, event) != nil {
		return record, ErrUnavailable
	}
	record.Events = []secretbroker.DeploymentEvent{{Sequence: 0, State: "pending", Effects: "unknown"}, event}
	if event.State != "succeeded" {
		return record, ErrUnavailable
	}
	return record, nil
}

// Reconcile reads the SAME request. It never resubmits or resolves unknown from
// preparation bytes. Caller authorizes current ownership, not old cookie identity.
func (c *Controller) Reconcile(ctx context.Context, record secretbroker.DeploymentRecord, target Target, authorize func(context.Context, secretbroker.Scope) error) (secretbroker.DeploymentEvent, error) {
	if c == nil || target == nil || authorize == nil || authorize(ctx, record.Intent.Identity.Scope) != nil || len(record.Events) == 0 {
		return secretbroker.DeploymentEvent{}, ErrUnavailable
	}
	records, err := c.service.State.Deployments(record.Intent.Identity.Scope)
	if err != nil {
		return secretbroker.DeploymentEvent{}, ErrUnavailable
	}
	found := false
	for _, r := range records {
		if r.Intent.ID == record.Intent.ID {
			record = r
			found = true
			break
		}
	}
	if !found {
		return secretbroker.DeploymentEvent{}, ErrUnavailable
	}
	intent := record.Intent
	last := record.Events[len(record.Events)-1]
	if last.State == "succeeded" || last.State == "failed" || last.State == "not-admitted" {
		return last, nil
	}
	info, err := target.Info(ctx)
	if err != nil || info.Journal != intent.JournalID || info.TargetDigest != intent.Reference.Scope.TargetDigest || info.EndpointDigest != intent.Reference.Scope.EndpointDigest {
		return last, ErrUnavailable
	}
	status, err := target.Status(ctx, intent.JournalID, intent.ID, intent.Reference)
	if err != nil || status.RequestID != intent.ID || status.Stack != intent.Identity.Scope.StackName {
		return last, ErrUnavailable
	}
	if status.State == last.State && status.Effects == last.Effects && status.OperationID == last.OperationID {
		return last, nil
	}
	next := secretbroker.DeploymentEvent{Sequence: last.Sequence + 1, State: status.State, Effects: status.Effects, OperationID: status.OperationID}
	if !next.Valid() || c.service.State.AppendDeploymentEvent(intent.Identity.Scope, intent.ID, last.Sequence, next) != nil {
		return last, ErrUnavailable
	}
	return next, nil
}
