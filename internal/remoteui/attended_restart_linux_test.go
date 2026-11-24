//go:build linux

package remoteui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/projectreview"
	"github.com/umayr/brine/internal/projectsnapshot"
	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/targetregistry"
)

func TestResolveRestoreLocatorAfterColdReopen(t *testing.T) {
	ctx := context.Background()
	root := realAttendedArchiveHome(t)
	for _, name := range []string{"archive", "key", "workspace"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	key := filepath.Join(root, "key", "generated")
	if err := os.WriteFile(key, []byte(strings.Repeat("k", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	options := snapshothistory.Options{ArchiveDir: filepath.Join(root, "archive"), KeyFile: key, WorkspaceDir: filepath.Join(root, "workspace"), StoreID: sha256.Sum256([]byte("restart-store"))}
	if err := snapshothistory.Initialize(ctx, options); err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(root, "registry")
	registry, err := targetregistry.Open(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	const host, stack = "generated", "synthetic"
	actor := archiveReadActor{operator: "synthetic", session: deploymentSession("fixture-session")}
	pairing := `{"version":1,"certificateSha256":"` + strings.Repeat("a", 64) + `","token":"` + strings.Repeat("A", 43) + `","agentPublicKey":"` + strings.Repeat("b", 64) + `","engineId":"11111111-1111-4111-8111-111111111111","generation":1,"engineKind":"docker","engineSocket":"unix:///run/docker.sock","daemonId":"synthetic"}`
	row := targetregistry.Record{ID: host, Revision: 1, HTTPSAddress: "127.0.0.1:1", HTTPSPairing: targetregistry.PairingSecret(pairing)}
	if err := registry.AddContext(ctx, row, row); err != nil {
		t.Fatal(err)
	}
	scopeOf := func(r *targetregistry.Registry) string {
		t.Helper()
		_, incarnation, lease, err := r.AcquireHistoryRead(ctx, host)
		if err != nil {
			t.Fatal(err)
		}
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(incarnation[:])
	}
	originalScope := scopeOf(registry)
	var activeEnrollment targetregistry.HistoryIncarnation
	_, _ = hex.Decode(activeEnrollment[:], []byte(originalScope))
	binding := attendedBinding{target: targetDigest(row), scope: originalScope, revision: strings.Repeat("c", 64), contractVersion: "dill/0.9.0", distinct: true, grants: attendedGrants}
	content := []byte("original-generated-archive-content")
	snapshot := projectsnapshot.Snapshot{Entrypoint: "main.pkl", Files: []projectsnapshot.File{{Path: "main.pkl", Content: content, SHA256: sha256.Sum256(content)}}}
	digest, err := dillserve.SnapshotManifestDigest(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := snapshothistory.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	locator := sha256.Sum256([]byte("generated-locator"))
	request := strings.Repeat("2", 32)
	ref, err := store.Commit(ctx, snapshothistory.CommitRequest{ObjectID: locator, Snapshot: snapshot, ExpectedManifestDigest: digest, Intent: snapshothistory.Intent{
		AttemptID: []byte(strings.Repeat("1", 32)), ProviderRequestID: []byte(request), OriginalJournal: []byte(attendedJournal), ApprovedTargetScope: []byte(originalScope),
		Attended: &snapshothistory.AttendedIntent{Target: binding.target, AuthorityRevision: binding.revision, Stack: stack, Mode: "create", Stage: attendedStage, Selection: attendedSelection},
	}})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(500, 0)
	if err := store.MarkDeliveryUnknown(ctx, ref, at); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTerminalObservation(ctx, ref, snapshothistory.TerminalObservation{
		Binding: snapshothistory.ReceiptBinding{Target: binding.target, Scope: originalScope, AuthorityRevision: binding.revision, Journal: attendedJournal, RequestID: request, Stack: stack},
		State:   "succeeded", Effects: "complete", OperationID: "op_" + strings.Repeat("3", 32), Provenance: "journal-apply", ContractVersion: binding.contractVersion, ObservedAt: at.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Recreate both handles; only the locator, not the original OpenRequest, is
	// supplied to the server prepare path from this point onward.
	reopened, err := snapshothistory.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	registry, err = targetregistry.Open(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	reviews, err := projectreview.New(8, 1<<20, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer reviews.Close()
	provider := &linuxAttendedProvider{t: t}
	var opens, checks, revokeAt int
	var duringOpen func()
	h := &Deployment{registry: registry, reviews: reviews, history: &attendedHistory{archive: reopened, archiveRead: newArchiveReadApprovals(),
		open: func(context.Context, targetregistry.Record) (attendedConnection, error) {
			opens++
			if duringOpen != nil {
				duringOpen()
			}
			return attendedConnection{client: provider, epoch: "fixture", binding: binding}, nil
		},
		restoreGate: func(_ context.Context, session string, current projectreview.Current, id snapshothistory.ObjectID) error {
			checks++
			if session != actor.session || current.Stack != stack || current.Target.Identity != binding.target || id != locator || (revokeAt > 0 && checks >= revokeAt) {
				return errors.New("fixture denied")
			}
			return nil
		},
	}}
	selection := archiveReadSelection{actor: actor, locator: locator, enrollment: activeEnrollment, target: binding.target, stack: stack, scope: originalScope}
	issue := func() string {
		t.Helper()
		id, err := h.history.archiveRead.issue(ctx, selection)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	approval := issue()
	preview, err := h.prepareRestoreReviewFromLocator(ctx, actor, approval, host, stack, locator)
	if err != nil || preview.ID == "" {
		t.Fatalf("cold locator review unavailable: %v", err)
	}
	token, err := reviews.Confirm(ctx, actor.session, preview.ID, preview.Current)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := reviews.Consume(ctx, actor.session, preview.ID, token, preview.Current)
	if err != nil || intent.PriorDeployment == nil || intent.PriorDeployment.ObjectID != locator || hex.EncodeToString(intent.PriorDeployment.RequestID[:]) != request || len(intent.Snapshot.Files) != 1 || string(intent.Snapshot.Files[0].Content) != string(content) {
		t.Fatalf("wrong authenticated review: %v", err)
	}

	assertDenied := func() {
		t.Helper()
		if p, err := h.prepareRestoreReviewFromLocator(ctx, actor, approval, host, stack, locator); !errors.Is(err, errRestoreReview) || p.ID != "" || provider.calls != 0 {
			t.Fatalf("unavailable binding produced a review: %v", err)
		}
	}
	assertDenied() // successful review consumed only the read approval
	approval = issue()
	duringOpen = func() { h.history.archiveRead.revoke(approval, actor) }
	assertDenied()
	duringOpen = nil
	approval = issue()
	before := opens
	revokeAt = checks + 1
	assertDenied()
	if opens != before {
		t.Fatal("revoked actor reached connection")
	}
	revokeAt = checks + 2
	assertDenied()
	if opens != before+1 {
		t.Fatal("revocation during connection was not exercised")
	}
	revokeAt = 0
	// The original locator still has a successful receipt. Inject a distinct
	// later unresolved operation in the same enrollment and stack.
	newIntent := ref.Intent
	newIntent.AttemptID = []byte(strings.Repeat("4", 32))
	newIntent.ProviderRequestID = []byte(strings.Repeat("5", 32))
	newRef, err := reopened.Commit(ctx, snapshothistory.CommitRequest{ObjectID: sha256.Sum256([]byte("later-intent")), Intent: newIntent, Snapshot: snapshot, ExpectedManifestDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.MarkDeliveryUnknown(ctx, newRef, at.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	afterNew, err := snapshothistory.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer afterNew.Close()
	h.history.archive = afterNew
	assertDenied()
	if err := registry.ForgetHTTPS(ctx, host); err != nil {
		t.Fatal(err)
	}
	if err := registry.AddContext(ctx, row, row); err != nil {
		t.Fatal(err)
	}
	binding.scope = scopeOf(registry)
	if binding.scope == originalScope {
		t.Fatal("fixture failed to rotate enrollment")
	}
	// Same host, stack, TLS identity, and grant revision, but new enrollment.
	assertDenied()
}
