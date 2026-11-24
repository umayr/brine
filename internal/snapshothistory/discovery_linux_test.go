//go:build linux

package snapshothistory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type exactScopeAuthorizer struct {
	scope  []byte
	denied bool
	calls  int
}

func (a *exactScopeAuthorizer) AuthorizeSnapshotHistoryRestart(ctx context.Context, scope []byte) error {
	a.calls++
	if ctx.Err() != nil || a.denied || !bytes.Equal(a.scope, scope) {
		return errors.New("denied")
	}
	return nil
}

func TestDiscoverRestartMetadataIsScopedPagedAndOpaque(t *testing.T) {
	o, _ := fixture(t, 1<<20, 10)
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	scope := []byte("synthetic-approved-target-scope")
	first, second, unrelated := request(30), request(31), request(32)
	first.Intent.ApprovedTargetScope, second.Intent.ApprovedTargetScope = append([]byte(nil), scope...), append([]byte(nil), scope...)
	unrelated.Intent.ApprovedTargetScope = []byte("other-scope")
	if _, err := s.Commit(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(context.Background(), unrelated); err != nil {
		t.Fatal(err)
	}
	auth := &exactScopeAuthorizer{scope: scope}
	page, err := s.DiscoverRestartMetadata(context.Background(), RestartDiscoveryRequest{ApprovedTargetScope: scope, Authorizer: auth, Limit: 1})
	if err != nil || auth.calls != 1 || len(page.Records) != 1 || !page.More {
		t.Fatalf("page=%+v calls=%d err=%v", page, auth.calls, err)
	}
	if len(page.Records[0].OriginalJournal) == 0 || page.Records[0].Locator == (ObjectID{}) {
		t.Fatal("missing authenticated metadata")
	}
	page2, err := s.DiscoverRestartMetadata(context.Background(), RestartDiscoveryRequest{ApprovedTargetScope: scope, Authorizer: auth, After: page.Next, Limit: 1})
	if err != nil || len(page2.Records) != 1 || !page2.More {
		t.Fatalf("page2=%+v err=%v", page2, err)
	}
	if page2.Records[0].Locator == page.Records[0].Locator {
		t.Fatal("pagination did not advance")
	}
	end, err := s.DiscoverRestartMetadata(context.Background(), RestartDiscoveryRequest{ApprovedTargetScope: scope, Authorizer: auth, After: page2.Next, Limit: 1})
	if err != nil || len(end.Records) != 0 || end.More || end.Next != "" {
		t.Fatalf("pagination did not end after other scope: %v", err)
	}
	// The API type carries no snapshot/path/hash fields; ensure it also created
	// no index or plaintext sidecar as a consequence of discovery.
	entries, err := os.ReadDir(o.ArchiveDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != identityName && !validObjectName(e.Name()) && !validPendingName(e.Name()) {
			t.Fatalf("unexpected sidecar %q", e.Name())
		}
		b, err := os.ReadFile(filepath.Join(o.ArchiveDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte("SYNTHETIC_SECRET_REVIEWED_BYTES")) {
			t.Fatal("plaintext archive bytes")
		}
	}
}

func TestDiscoverRestartMetadataRejectsScopeDenialTamperAndBounds(t *testing.T) {
	o, _ := fixture(t, 1<<20, 10)
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := request(33)
	r.Intent.ApprovedTargetScope = []byte("scope-a")
	if _, err := s.Commit(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	denied := &exactScopeAuthorizer{scope: []byte("scope-a"), denied: true}
	if page, err := s.DiscoverRestartMetadata(context.Background(), RestartDiscoveryRequest{ApprovedTargetScope: []byte("scope-a"), Authorizer: denied, Limit: 1}); err == nil || len(page.Records) != 0 {
		t.Fatal("denied discovery exposed metadata")
	}
	wrong := &exactScopeAuthorizer{scope: []byte("scope-b")}
	if page, err := s.DiscoverRestartMetadata(context.Background(), RestartDiscoveryRequest{ApprovedTargetScope: []byte("scope-b"), Authorizer: wrong, Limit: 1}); err != nil || len(page.Records) != 0 {
		t.Fatalf("wrong scope page=%+v err=%v", page, err)
	}
	if _, err := s.DiscoverRestartMetadata(context.Background(), RestartDiscoveryRequest{ApprovedTargetScope: []byte("scope-a"), Authorizer: &exactScopeAuthorizer{scope: []byte("scope-a")}, Limit: maxRestartDiscoveryResults + 1}); err == nil {
		t.Fatal("unbounded page accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if page, err := s.DiscoverRestartMetadata(ctx, RestartDiscoveryRequest{ApprovedTargetScope: []byte("scope-a"), Authorizer: &exactScopeAuthorizer{scope: []byte("scope-a")}, Limit: 1}); err == nil || len(page.Records) != 0 {
		t.Fatal("cancelled discovery returned partial result")
	}
	path := filepath.Join(o.ArchiveDir, objectName(r.ObjectID))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if page, err := s.DiscoverRestartMetadata(context.Background(), RestartDiscoveryRequest{ApprovedTargetScope: []byte("scope-a"), Authorizer: &exactScopeAuthorizer{scope: []byte("scope-a")}, Limit: 1}); err == nil || len(page.Records) != 0 {
		t.Fatal("tampered discovery returned partial metadata")
	}
}
