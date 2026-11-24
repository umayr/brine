//go:build linux

package snapshothistory

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewSummaryRebuildCountsEachUnresolvedObjectAndDoesNoIO(t *testing.T) {
	ctx := context.Background()
	o, s, first, receipt := receiptFixture(t, 10)
	defer s.Close()
	second := nextAttendedCommit(first, 78, first.Intent.Attended.Stack)
	second.Intent.OriginalJournal = first.Intent.OriginalJournal
	second.Intent.ProviderRequestID = []byte(strings.Repeat("8", 32))
	// Seed an authenticated second old-format object, as a pre-guard archive
	// could already contain two overlaps. Reopen must derive both from disk.
	p := payload{Version: payloadVersion, ObjectID: hex.EncodeToString(second.ObjectID[:]), AttemptID: second.Intent.AttemptID, ProviderRequestID: second.Intent.ProviderRequestID, OriginalJournal: second.Intent.OriginalJournal, ApprovedTargetScope: second.Intent.ApprovedTargetScope, Attended: second.Intent.Attended, ManifestDigest: hex.EncodeToString(second.ExpectedManifestDigest[:]), Snapshot: second.Snapshot}
	plain, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	data, err := seal(s.impl.key, s.impl.storeID, second.ObjectID, "object", plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeNoClobber(s.impl.root, objectName(second.ObjectID), data); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	scope, stack := string(first.Intent.ApprovedTargetScope), first.Intent.Attended.Stack
	key := operationKey([]byte(scope), stack)
	if n := s.impl.attended.counts[key]; n != 2 {
		t.Fatalf("rebuilt count %d; want 2", n)
	}
	if err := s.MarkDeliveryUnknown(ctx, first, receipt.ObservedAt); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTerminalObservation(ctx, first, receipt); err != nil {
		t.Fatal(err)
	}
	if n := s.impl.attended.counts[key]; n != 1 {
		t.Fatalf("one object's receipt produced count %d; want 1", n)
	}
	// The preview summary must not try to re-open even the root, let alone scan.
	// Invalidate only the handle's lookup path; leave the real generated archive
	// and flock unchanged. The final authoritative inventory must still fail.
	originalPath := s.impl.rootPath
	s.impl.rootPath = filepath.Join(o.ArchiveDir, "nonexistent")
	if err := s.CheckAttendedIdle(ctx, scope, stack); !errors.Is(err, ErrUnresolvedOperation) {
		t.Fatalf("preview did not use retained summary: %v", err)
	}
	if err := s.CheckAttendedIdle(ctx, scope, "idle-other-stack"); err != nil {
		t.Fatalf("idle preview attempted archive I/O: %v", err)
	}
	if _, err := s.Commit(ctx, nextAttendedCommit(first, 79, "idle-other-stack")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("commit bypassed inventory check: %v", err)
	}
	s.impl.rootPath = originalPath
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// A foreign/invalid retained entry must not create a usable partial summary.
	if err := os.WriteFile(filepath.Join(o.ArchiveDir, "unexpected"), []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if incomplete, err := Open(ctx, o); incomplete != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("incomplete rebuild published a summary: %v", err)
	}
}

func TestPreviewSummaryPoisonedAfterAmbiguousReceiptUntilReopen(t *testing.T) {
	ctx := context.Background()
	o, s, ref, receipt := receiptFixture(t, 10)
	scope, stack := string(ref.Intent.ApprovedTargetScope), ref.Intent.Attended.Stack
	if err := s.MarkDeliveryUnknown(ctx, ref, receipt.ObservedAt); err != nil {
		t.Fatal(err)
	}
	testDirectorySync = func(int) error { return errors.New("generated sync failure") }
	defer func() { testDirectorySync = nil }()
	err := s.RecordTerminalObservation(ctx, ref, receipt)
	testDirectorySync = nil
	if !errors.Is(err, ErrIndeterminate) {
		t.Fatalf("ambiguous receipt not reported: %v", err)
	}
	if err := s.CheckAttendedIdle(ctx, scope, stack); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("indeterminate handle allowed preview: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if evidence, err := reopened.ReadEvidence(ctx, ref); err != nil || evidence.State != VerifiedReceipt {
		t.Fatalf("reopened did not authenticate visible receipt: %v", err)
	}
	if err := reopened.CheckAttendedIdle(ctx, scope, stack); err != nil {
		t.Fatalf("successful reopen did not rebuild idle state: %v", err)
	}
}
