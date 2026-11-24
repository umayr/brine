//go:build linux

package snapshothistory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func receiptFixture(t *testing.T, maxOps int) (Options, *Store, OpenRequest, TerminalObservation) {
	t.Helper()
	ctx := context.Background()
	o, _ := fixture(t, 1<<20, maxOps)
	s, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	r := request(99)
	r.Intent.OriginalJournal = []byte(strings.Repeat("1", 32))
	r.Intent.ProviderRequestID = []byte(strings.Repeat("2", 32))
	r.Intent.ApprovedTargetScope = []byte("generated-scope-original")
	r.Intent.Attended = &AttendedIntent{Target: strings.Repeat("a", 64), AuthorityRevision: strings.Repeat("b", 64), Stack: "synthetic", Mode: "update", Stage: strings.Repeat("c", 32), Selection: "sha256:" + strings.Repeat("d", 64)}
	ref, err := s.Commit(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	result := TerminalObservation{Binding: ReceiptBinding{Target: r.Intent.Attended.Target, Scope: string(r.Intent.ApprovedTargetScope), AuthorityRevision: r.Intent.Attended.AuthorityRevision, Journal: string(r.Intent.OriginalJournal), RequestID: string(r.Intent.ProviderRequestID), Stack: r.Intent.Attended.Stack}, State: "succeeded", Effects: "complete", OperationID: "op_" + strings.Repeat("3", 32), Provenance: "journal-status", ContractVersion: "dill/0.9.0", ObservedAt: time.Unix(500, 0).UTC()}
	return o, s, ref, result
}

func TestAttendedEvidencePersistsDistinctStatesAndOriginalBinding(t *testing.T) {
	ctx := context.Background()
	o, s, ref, r := receiptFixture(t, 1)
	at := r.ObservedAt.Add(-time.Second)
	check := func(s *Store, state DeliveryState) Evidence {
		t.Helper()
		got, err := s.ReadEvidence(ctx, ref)
		if err != nil || got.State != state {
			t.Fatalf("wanted %s: %v (got %s)", state, err, got.State)
		}
		return got
	}
	check(s, LocalIntentOnly)
	if err := s.RecordTerminalObservation(ctx, ref, r); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("receipt without marker: %v", err)
	}
	if err := s.MarkDeliveryUnknown(ctx, ref, at); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDeliveryUnknown(ctx, ref, at); !errors.Is(err, ErrExists) {
		t.Fatalf("marker overwritten: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	mid, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer mid.Close()
	if got := check(mid, DeliveryUnknown); got.UnknownSince != at || got.Receipt != nil {
		t.Fatal("bad unknown projection")
	}
	if err := mid.RecordTerminalObservation(ctx, ref, r); err != nil {
		t.Fatal(err)
	}
	contradiction := r
	contradiction.State, contradiction.Effects = "failed", "partial"
	if err := mid.RecordTerminalObservation(ctx, ref, contradiction); !errors.Is(err, ErrExists) {
		t.Fatalf("receipt overwritten: %v", err)
	}
	if err := mid.Close(); err != nil {
		t.Fatal(err)
	}
	end, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer end.Close()
	got := check(end, VerifiedReceipt)
	if got.Receipt == nil || *got.Receipt != r || got.UnknownSince != at {
		t.Fatal("verified evidence changed across reopen")
	}
	for _, kind := range []string{evidenceMarker, evidenceReceipt} {
		data, err := os.ReadFile(filepath.Join(o.ArchiveDir, evidenceName(ref.ObjectID, kind)))
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range []string{r.Binding.Scope, r.Binding.Journal, r.Binding.RequestID, r.OperationID, r.ContractVersion} {
			if bytes.Contains(data, []byte(value)) {
				t.Fatal("plaintext binding on disk")
			}
		}
	}
}

func TestAttendedEvidenceRejectsUnboundOrNonTerminalObservation(t *testing.T) {
	ctx := context.Background()
	_, s, ref, r := receiptFixture(t, 10)
	if err := s.MarkDeliveryUnknown(ctx, ref, r.ObservedAt); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*TerminalObservation)
	}{
		{"target", func(x *TerminalObservation) { x.Binding.Target = strings.Repeat("8", 64) }},
		{"scope", func(x *TerminalObservation) { x.Binding.Scope = "different-scope" }},
		{"grant revision", func(x *TerminalObservation) { x.Binding.AuthorityRevision = strings.Repeat("8", 64) }},
		{"journal", func(x *TerminalObservation) { x.Binding.Journal = strings.Repeat("8", 32) }},
		{"request", func(x *TerminalObservation) { x.Binding.RequestID = strings.Repeat("8", 32) }},
		{"stack", func(x *TerminalObservation) { x.Binding.Stack = "different-stack" }},
		{"missing operation", func(x *TerminalObservation) { x.OperationID = "" }},
		{"request substituted for operation", func(x *TerminalObservation) { x.OperationID = x.Binding.RequestID }},
		{"pending", func(x *TerminalObservation) { x.State, x.Effects, x.OperationID = "running", "unknown", "" }},
		{"unknown", func(x *TerminalObservation) { x.State, x.Effects, x.OperationID = "unknown", "unknown", "" }},
		{"fence", func(x *TerminalObservation) { x.State, x.Effects, x.OperationID = "not-admitted", "none", "" }},
		{"wrong source", func(x *TerminalObservation) { x.Provenance = "generic-status" }},
		{"old", func(x *TerminalObservation) { x.ObservedAt = x.ObservedAt.Add(-time.Second) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := r
			tc.change(&bad)
			if err := s.RecordTerminalObservation(ctx, ref, bad); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("bad binding accepted: %v", err)
			}
			if got, err := s.ReadEvidence(ctx, ref); err != nil || got.State != DeliveryUnknown || got.Receipt != nil {
				t.Fatal("unknown state overwritten")
			}
		})
	}
	other := ref
	other.Intent.ApprovedTargetScope = []byte("another-scope")
	if _, err := s.ReadEvidence(ctx, other); !errors.Is(err, ErrUnavailable) {
		t.Fatal("cross-scope read")
	}
}

func TestAttendedEvidencePostPublishAmbiguityAndCorruption(t *testing.T) {
	ctx := context.Background()
	o, s, ref, r := receiptFixture(t, 10)
	testFault = func(stage string) error {
		if stage == "after-rename-before-directory-sync" {
			return errors.New("generated publication cut")
		}
		return nil
	}
	defer func() { testFault = nil }()
	err := s.MarkDeliveryUnknown(ctx, ref, r.ObservedAt)
	testFault = nil
	if !errors.Is(err, ErrIndeterminate) {
		t.Fatalf("marker publication uncertainty lost: %v", err)
	}
	if _, err := s.ReadEvidence(ctx, ref); !errors.Is(err, ErrUnavailable) {
		t.Fatal("failed handle allowed evidence read")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.ReadEvidence(ctx, ref); err != nil || got.State != DeliveryUnknown {
		t.Fatalf("visible marker not preserved: %v", err)
	}
	if err := reopened.RecordTerminalObservation(ctx, ref, r); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(o.ArchiveDir, evidenceName(ref.ObjectID, evidenceReceipt))
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(ctx, o); !errors.Is(err, ErrUnavailable) {
		if opened != nil {
			_ = opened.Close()
		}
		t.Fatalf("corrupt evidence accepted: %v", err)
	}
}
