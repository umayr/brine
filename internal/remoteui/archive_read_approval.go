package remoteui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/projectreview"
	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/targetregistry"
)

const maxArchiveReadApprovals = 64
const archiveReadApprovalTTL = 5 * time.Minute
const archiveReadIDPrefix = "archive-read-"

type archiveReadActor struct{ operator, session string }

func (h *Deployment) archiveReadActor(r *http.Request) (archiveReadActor, bool) {
	if h.auth == nil || r == nil || !auth.CookieAuthenticated(r) {
		return archiveReadActor{}, false
	}
	actor := archiveReadActor{operator: auth.Identity(r), session: deploymentSession(h.auth.CSRFFor(r))}
	return actor, actor.operator != ""
}

// approveArchiveRead is private to a future protected explicit-consent flow.
// reviewed must be supplied by trusted server-side state under the active
// enrollment lease, not assembled from mutable request fields.
func (h *Deployment) approveArchiveRead(r *http.Request, reviewed archiveReadSelection) (string, error) {
	actor, ok := h.archiveReadActor(r)
	if !ok || r.Method != http.MethodPost || actor != reviewed.actor || h.history == nil || h.history.operator != nil {
		return "", errRestoreReview
	}
	ctx, release, ok := h.auth.SubmissionLease(r)
	if !ok {
		return "", errRestoreReview
	}
	defer release()
	return h.history.archiveRead.issue(ctx, reviewed)
}

// reviewApprovedArchive derives identity from authenticated middleware and
// holds the session lease while preparing a local review. No routes call it.
func (h *Deployment) reviewApprovedArchive(r *http.Request, approval, host, stack string, locator snapshothistory.ObjectID) (projectreview.Preview, error) {
	actor, ok := h.archiveReadActor(r)
	if !ok || r.Method != http.MethodPost {
		return projectreview.Preview{}, errRestoreReview
	}
	ctx, release, ok := h.auth.SubmissionLease(r)
	if !ok {
		return projectreview.Preview{}, errRestoreReview
	}
	defer release()
	return h.prepareRestoreReviewFromLocator(ctx, actor, approval, host, stack, locator)
}

func deploymentSession(csrf string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte("brine-remote-deployment\x00"+csrf)))
}

type archiveReadSelection struct {
	actor                archiveReadActor
	locator              snapshothistory.ObjectID
	enrollment           targetregistry.HistoryIncarnation
	target, stack, scope string
}

func newArchiveReadSelection(actor archiveReadActor, locator snapshothistory.ObjectID, enrollment targetregistry.HistoryIncarnation, c projectreview.Current) archiveReadSelection {
	return archiveReadSelection{actor: actor, locator: locator, enrollment: enrollment, target: c.Target.Identity, stack: c.Stack, scope: hex.EncodeToString(enrollment[:])}
}

// archiveReadApprovals is bounded and process-local. Absence after restart is
// denial. A random ID selects one explicit permission to prepare a fresh
// review, not a transfer confirmation, deployment approval, or grant.
type archiveReadApprovals struct {
	mu      sync.Mutex
	entries map[string]archiveReadApproval
	now     func() time.Time
}

type archiveReadApproval struct {
	selection archiveReadSelection
	expires   time.Time
}

func newArchiveReadApprovals() *archiveReadApprovals {
	return &archiveReadApprovals{entries: make(map[string]archiveReadApproval), now: time.Now}
}

// issue must only be called after explicit local operator consent to the exact
// server-derived selection. There is deliberately no generic issue/read route.
func (s *archiveReadApprovals) issue(ctx context.Context, q archiveReadSelection) (string, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || q.actor.operator == "" || len(q.actor.operator) > 256 || !attendedHex(q.actor.session, 32) || q.locator == (snapshothistory.ObjectID{}) ||
		q.enrollment == (targetregistry.HistoryIncarnation{}) || !attendedHex(q.target, 32) || q.scope != hex.EncodeToString(q.enrollment[:]) || q.stack == "" || len(q.stack) > 128 {
		return "", errRestoreReview
	}
	id, err := agentclient.NewRequestID()
	if err != nil {
		return "", errRestoreReview
	}
	id = archiveReadIDPrefix + id
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, a := range s.entries {
		if !now.Before(a.expires) {
			delete(s.entries, k)
		}
	}
	if ctx.Err() != nil || len(s.entries) >= maxArchiveReadApprovals || s.entries[id] != (archiveReadApproval{}) {
		return "", errRestoreReview
	}
	s.entries[id] = archiveReadApproval{selection: q, expires: now.Add(archiveReadApprovalTTL)}
	return id, nil
}

// use is checked before protected work and again atomically at publication.
// A revoked/expired grant cannot publish an already prepared review.
func (s *archiveReadApprovals) use(ctx context.Context, id string, q archiveReadSelection, consume bool) bool {
	if s == nil || ctx == nil || ctx.Err() != nil || !strings.HasPrefix(id, archiveReadIDPrefix) || !attendedHex(strings.TrimPrefix(id, archiveReadIDPrefix), 16) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.entries[id]
	if !ok || q != a.selection || !s.now().Before(a.expires) || ctx.Err() != nil {
		return false
	}
	if consume {
		delete(s.entries, id)
	}
	return true
}

func (s *archiveReadApprovals) revoke(id string, actor archiveReadActor) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.entries[id]; ok && actor == a.selection.actor {
		delete(s.entries, id)
	}
}
