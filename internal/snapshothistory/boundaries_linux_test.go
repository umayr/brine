//go:build linux

package snapshothistory

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveDirectoryCeilingPreservesLastLegalCommit(t *testing.T) {
	ctx := context.Background()
	o, _ := fixture(t, 1<<20, maxArchiveEntries-1)
	s, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, err := s.Commit(ctx, request(61))
	if err != nil {
		t.Fatal(err)
	}
	// Identity, an existing object, and generated pending names model retained
	// remnants. Leave exactly one slot for the temporary name and its rename.
	for i := 0; i < maxArchiveEntries-3; i++ {
		name := filepath.Join(o.ArchiveDir, fmt.Sprintf("%s%032x", pendingPrefix, i))
		if err := os.WriteFile(name, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	last, err := s.Commit(ctx, request(62))
	if err != nil {
		t.Fatalf("last legal commit: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, o)
	if err != nil {
		t.Fatalf("reopen at entry ceiling: %v", err)
	}
	defer reopened.Close()
	refused := request(63)
	if _, err := reopened.Commit(ctx, refused); !errors.Is(err, ErrCapacity) {
		t.Fatalf("over-limit commit: %v", err)
	}
	names, err := os.ReadDir(o.ArchiveDir)
	if err != nil || len(names) != maxArchiveEntries {
		t.Fatalf("refusal left an extra entry: count=%d err=%v", len(names), err)
	}
	if _, err := os.Stat(filepath.Join(o.ArchiveDir, objectName(refused.ObjectID))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused object published: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(ctx, o)
	if err != nil {
		t.Fatalf("refusal poisoned reopen: %v", err)
	}
	defer again.Close()
	for _, id := range []OpenRequest{first, last} {
		if _, err := again.Open(ctx, id); err != nil {
			t.Fatalf("prior commit not readable: %v", err)
		}
	}
}

func TestUnsupportedOperationCapRejectedBeforeWrites(t *testing.T) {
	o, _ := fixture(t, 1<<20, 10)
	o.ArchiveDir = filepath.Join(filepath.Dir(o.ArchiveDir), "uninitialized")
	if err := os.Mkdir(o.ArchiveDir, 0700); err != nil {
		t.Fatal(err)
	}
	o.MaxOperations = maxArchiveEntries
	if err := Initialize(context.Background(), o); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unsupported cap accepted: %v", err)
	}
	files, err := os.ReadDir(o.ArchiveDir)
	if err != nil || len(files) != 0 {
		t.Fatalf("invalid config wrote archive state: count=%d err=%v", len(files), err)
	}
}

func TestRestartDiscoveryAdvancesPastObjectBudgetAndBindsCursor(t *testing.T) {
	ctx := context.Background()
	o, _ := fixture(t, 1<<20, maxRestartDiscoveryObjects+1)
	s, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	scope := []byte("selected-synthetic-scope")
	var selected OpenRequest
	for n := 1; n <= maxRestartDiscoveryObjects+1; n++ {
		r := request(64)
		binary.BigEndian.PutUint64(r.ObjectID[:8], uint64(n))
		r.Intent.AttemptID = []byte(fmt.Sprintf("attempt-%d", n))
		r.Intent.ProviderRequestID = []byte(fmt.Sprintf("request-%d", n))
		if n == maxRestartDiscoveryObjects+1 {
			r.Intent.ApprovedTargetScope = scope
		}
		selected, err = s.Commit(ctx, r)
		if err != nil {
			t.Fatalf("commit %d: %v", n, err)
		}
	}
	q := RestartDiscoveryRequest{ApprovedTargetScope: scope, Authorizer: &exactScopeAuthorizer{scope: scope}, Limit: 1}
	page, err := s.DiscoverRestartMetadata(ctx, q)
	if err != nil || !page.More || page.Next == "" || len(page.Records) != 0 {
		t.Fatalf("scope scan cannot advance after empty page: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	foreign := []byte("foreign-scope")
	if _, err := current.DiscoverRestartMetadata(ctx, RestartDiscoveryRequest{ApprovedTargetScope: foreign, Authorizer: &exactScopeAuthorizer{scope: foreign}, After: page.Next, Limit: 1}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("foreign cursor accepted: %v", err)
	}
	q.After = "!" + page.Next[1:]
	if _, err := current.DiscoverRestartMetadata(ctx, q); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("tampered cursor accepted: %v", err)
	}
	q.After = page.Next
	end, err := current.DiscoverRestartMetadata(ctx, q)
	if err != nil || end.More || end.Next != "" || len(end.Records) != 1 || end.Records[0].Locator != selected.ObjectID {
		t.Fatalf("selected record was stranded after restart: %v", err)
	}
}

func TestRestartDiscoveryAdvancesAtCiphertextBudget(t *testing.T) {
	ctx := context.Background()
	o, _ := fixture(t, 1<<20, 10)
	s, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	foreign, selected := request(65), request(66)
	for _, r := range []CommitRequest{foreign, selected} {
		if _, err := s.Commit(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	a, err := s.impl.sealedSize(objectName(foreign.ObjectID))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.impl.sealedSize(objectName(selected.ObjectID))
	if err != nil {
		t.Fatal(err)
	}
	// Inject a smaller byte allowance into the same scanner, not the archive.
	q := RestartDiscoveryRequest{ApprovedTargetScope: selected.Intent.ApprovedTargetScope, Limit: 1}
	page, err := s.impl.discoverRestartMetadata(ctx, q, max(a, b))
	if err != nil || !page.More || len(page.Records) != 0 {
		t.Fatalf("byte-budget cursor not returned: %v", err)
	}
	q.After = page.Next
	end, err := s.impl.discoverRestartMetadata(ctx, q, max(a, b))
	if err != nil || end.More || len(end.Records) != 1 || end.Records[0].Locator != selected.ObjectID {
		t.Fatalf("selected record skipped at byte boundary: %v", err)
	}
}
