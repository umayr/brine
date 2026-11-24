package remoteui

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/projectsnapshot"
	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/targetregistry"
)

const maxAttendedReviewBytes = 64 << 20

const (
	attendedStageGrant = 1 << iota
	attendedManifestGrant
	attendedPreviewGrant
	attendedJournalApplyGrant
	attendedGrants = attendedStageGrant | attendedManifestGrant | attendedPreviewGrant | attendedJournalApplyGrant
)

// These dependencies are private and intentionally not mounted by NewDeployment
// or the server. Only a trusted, qualified authority can construct an attended
// client; the legacy single-bearer client must not implement this opener.
// Open must verify a current, distinct deploy grant bound to the signed monitor
// identity, endpoint, target and credential revision (not probe with a write).
type attendedHistory struct {
	archive interface {
		Commit(context.Context, snapshothistory.CommitRequest) (snapshothistory.OpenRequest, error)
	}
	open func(context.Context, targetregistry.Record) (attendedConnection, error)
}

type deploymentStager interface {
	Stage(context.Context, string, []byte) (string, error)
	Snapshot(context.Context, string, string) (dillserve.Manifest, error)
	PreviewDeployment(context.Context, string, string, string, string) (*agentclient.DeploymentPreview, error)
}

type attendedClient interface {
	deploymentStager
	ApplyJournalDeployment(context.Context, string, string, string, string, string, string, string, string) (agentclient.DeploymentStatus, error)
	Close() error
}

type attendedBinding struct {
	target, scope, revision string // opaque non-secret identities, not credentials
	grants                  uint8
	distinct                bool
}

type attendedConnection struct {
	client  attendedClient
	epoch   string
	binding attendedBinding
}

type attendedReview struct {
	snapshot  projectsnapshot.Snapshot
	bytes     int
	binding   attendedBinding
	manifest  [sha256.Size]byte
	journal   string
	requestID string
	timer     *time.Timer
}

func (h *attendedHistory) connection(ctx context.Context, record targetregistry.Record, expected string) (attendedConnection, error) {
	if h == nil || h.open == nil || h.archive == nil || ctx.Err() != nil {
		return attendedConnection{}, errors.New("attended history unavailable")
	}
	c, err := h.open(ctx, record)
	b := c.binding
	if err != nil || c.client == nil || c.epoch == "" || ctx.Err() != nil || !b.distinct || b.grants&attendedGrants != attendedGrants ||
		b.target != expected || !attendedHex(b.target, 32) || !attendedHex(b.revision, 32) || len(b.scope) == 0 || len(b.scope) > 512 {
		if c.client != nil {
			_ = c.client.Close()
		}
		return attendedConnection{}, errors.New("attended authority unavailable")
	}
	return c, nil
}

func attendedHex(input string, bytes int) bool {
	if len(input) != bytes*2 {
		return false
	}
	b, err := hex.DecodeString(input)
	return err == nil && hex.EncodeToString(b) == input
}

func newAttendedReview(s projectsnapshot.Snapshot, binding attendedBinding, stage, manifest, stack string, plan *agentclient.DeploymentPreview) *attendedReview {
	if plan == nil || plan.PreflightResult == nil || plan.Stack != stack || plan.JournalCapacityLow ||
		(plan.Mode != "create" && plan.Mode != "update") || !attendedHex(plan.JournalID, 16) ||
		!attendedHex(stage, 16) || !attendedHex(manifest, 32) || plan.SelectionDigest == nil ||
		!strings.HasPrefix(*plan.SelectionDigest, "sha256:") || !attendedHex(strings.TrimPrefix(*plan.SelectionDigest, "sha256:"), 32) {
		return nil
	}
	id, err := agentclient.NewRequestID()
	if err != nil {
		return nil
	}
	digest, _ := hex.DecodeString(manifest)
	var a attendedReview
	copy(a.manifest[:], digest)
	a.snapshot, a.binding, a.journal, a.requestID = s, binding, plan.JournalID, id
	// Bound bytes and metadata. Capture itself is separately capped by the
	// project review store; this accounts for the retained copy it transfers.
	a.bytes = len(s.Entrypoint)
	for _, f := range s.Files {
		a.bytes += len(f.Path) + len(f.Content) + 128
	}
	return &a
}

func (h *Deployment) submitAttended(ctx context.Context, w http.ResponseWriter, p deploymentPage, r deploymentReview, record targetregistry.Record) {
	p.Attended = true
	a := r.Attended
	if a == nil || a.requestID == p.ID || !time.Now().Before(r.Expiry) {
		http.Error(w, "Attended history review unavailable; no operation sent", http.StatusConflict)
		return
	}
	c, err := h.history.connection(ctx, record, r.TargetDigest)
	if err != nil {
		http.Error(w, "Attended authority unavailable; no operation sent", http.StatusConflict)
		return
	}
	defer c.client.Close()
	if c.binding != a.binding || !time.Now().Before(r.Expiry) {
		http.Error(w, "Attended authority changed; no operation sent", http.StatusConflict)
		return
	}
	var objectID snapshothistory.ObjectID
	if _, err := rand.Read(objectID[:]); err != nil {
		http.Error(w, "Attended history unavailable; no operation sent", http.StatusServiceUnavailable)
		return
	}
	intent := snapshothistory.Intent{
		AttemptID: []byte(p.ID), ProviderRequestID: []byte(a.requestID), OriginalJournal: []byte(a.journal), ApprovedTargetScope: []byte(a.binding.scope),
		Attended: &snapshothistory.AttendedIntent{Target: r.TargetDigest, AuthorityRevision: a.binding.revision, Stack: r.Stack, Mode: r.Mode, Stage: r.StageID, Selection: r.Selection},
	}
	if ctx.Err() != nil {
		http.Error(w, "Attended request canceled; no operation sent", http.StatusConflict)
		return
	}
	_, err = h.history.archive.Commit(ctx, snapshothistory.CommitRequest{ObjectID: objectID, Intent: intent, ExpectedManifestDigest: a.manifest, Snapshot: a.snapshot})
	if errors.Is(err, snapshothistory.ErrIndeterminate) {
		http.Error(w, "Local history retention is uncertain; preserve it for inspection. No operation was sent. Do not retry.", http.StatusServiceUnavailable)
		return
	}
	if err != nil || ctx.Err() != nil || !time.Now().Before(r.Expiry) {
		http.Error(w, "Attended history could not authorize submission; no operation sent", http.StatusServiceUnavailable)
		return
	}
	p.ID = a.requestID
	status, err := c.client.ApplyJournalDeployment(ctx, c.epoch, r.StageID, r.Manifest, r.Stack, r.Selection, a.requestID, r.Mode, a.journal)
	if err != nil || status.RequestID != a.requestID || status.Stack != r.Stack {
		p.Error = "Operation not confirmed. No retry was sent. Do not retry based on this result."
		p.JournalCapacityLow = errors.Is(err, agentclient.ErrJournalCapacity)
	} else {
		// The authenticated live response is not a durable receipt. No history
		// status or restore path is mounted by this unqualified seam.
		p.Status = &status
	}
	h.render(w, p)
}
