package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/webhooks"
)

func metadataEndpoint(t *testing.T, s *Server) webhooks.Endpoint {
	t.Helper()
	ep, _, e := s.webhooks.SaveEndpoint(context.Background(), webhooks.Endpoint{Name: "metadata", URL: "https://hooks.example.test/source", Events: []string{"stack.created", "stack.updated", "stack.deleted", "source.revision_created", "source.restored"}, Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	return ep
}
func TestAuthoritativeMetadataEventsAreNotCoalescedOrLeaked(t *testing.T) {
	ctx := context.Background()
	s, st, _, _ := dispatcherServer(t)
	if e := s.configureWebhookMetadata(ctx, s.webhooks); e != nil {
		t.Fatal(e)
	}
	ep := metadataEndpoint(t, s)
	created, e := st.Create("observed", "// private-source-fixture-do-not-export\n")
	if e != nil {
		t.Fatal(e)
	}
	if created.EventHead == "" {
		t.Fatal("metadata commit marker absent")
	}
	if _, e = st.Update("observed", "// private-source-fixture-do-not-export\n// next\n"); e != nil {
		t.Fatal(e)
	}
	if e = st.WriteFile("observed", "extra.pkl", "private-source-fixture-do-not-export"); e != nil {
		t.Fatal(e)
	}
	if e = st.DeleteFile("observed", "extra.pkl"); e != nil {
		t.Fatal(e)
	}
	req := httptest.NewRequest("GET", "/api/stacks/observed", nil)
	req.Header.Set("Authorization", "Bearer fixture-token")
	response := httptest.NewRecorder()
	s.router.ServeHTTP(response, req)
	if response.Code != 200 || bytes.Contains(response.Body.Bytes(), []byte("event_head")) {
		t.Fatal("internal marker leaked through stack metadata", response.Code)
	}
	if e = st.Delete("observed"); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e = s.webhooks.ReconcileMetadata(ctx); e != nil {
			t.Fatal(e)
		}
	}
	deliveries, e := s.webhooks.Deliveries(ctx, ep.ID)
	if e != nil || len(deliveries) != 5 {
		t.Fatalf("lost/duplicated committed metadata transitions: %d %v", len(deliveries), e)
	}
	counts := map[string]int{}
	for _, d := range deliveries {
		counts[d.Event.Type]++
		b, _ := json.Marshal(d.Event)
		if bytes.Contains(b, []byte("private-source-fixture")) {
			t.Fatal("source contents exported")
		}
		if d.Event.Target.Kind != "none" {
			t.Fatal("source mutation inferred execution target activity")
		}
	}
	if counts["stack.created"] != 1 || counts["stack.updated"] != 3 || counts["stack.deleted"] != 1 {
		t.Fatal("incorrect transition types", counts)
	}
}
func TestStagedGitCreatePublishesOnlyFinalRegistration(t *testing.T) {
	ctx := context.Background()
	s, st, _, _ := dispatcherServer(t)
	if e := s.configureWebhookMetadata(ctx, s.webhooks); e != nil {
		t.Fatal(e)
	}
	ep := metadataEndpoint(t, s)
	stack, e := st.CreateGitAttempt(ctx, "new-git", &store.GitSource{URL: "https://source.example.test/repo?fixture-private-token", Branch: "main"}, "compose.pkl", func(_ context.Context, dir string) error {
		if e := os.MkdirAll(dir, 0700); e != nil {
			return e
		}
		return os.WriteFile(filepath.Join(dir, "compose.pkl"), []byte("// private fixture source\n"), 0600)
	})
	if e != nil {
		t.Fatal(e)
	}
	current, e := st.Get("new-git")
	if e != nil || current.EventHead == "" || current.EventHead != stack.EventHead {
		t.Fatal("atomic publication lost marker", e)
	}
	if e = s.webhooks.ReconcileMetadata(ctx); e != nil {
		t.Fatal(e)
	}
	ds, _ := s.webhooks.Deliveries(ctx, ep.ID)
	if len(ds) != 1 || ds[0].Event.Type != "stack.created" || ds[0].Event.Source.Kind != "git" {
		t.Fatal("staging emitted phantom or missing registration", ds)
	}
	b, _ := json.Marshal(ds)
	if bytes.Contains(b, []byte("fixture-private-token")) {
		t.Fatal("Git credentials leaked")
	}
}
func TestMetadataCommitRecoveredWithoutCallbackOrReplay(t *testing.T) {
	ctx := context.Background()
	s, st, _, _ := dispatcherServer(t)
	ep := metadataEndpoint(t, s)
	// Simulate lost callbacks: metadata still carries the exact durable intent ID.
	st.SetMetadataObserver(func(c store.MetadataChange) (store.MetadataCommit, error) {
		after := c.After
		before := ""
		if c.Before != nil {
			before = c.Before.EventHead
		}
		event := webhooks.Event{Type: "stack.updated", Stack: webhooks.Resource{ID: webhookStackID(after), Name: after.Name}}
		if c.Before == nil {
			event.Type = "stack.created"
		}
		id, e := s.webhooks.BeginMetadataMutation(ctx, after.Name, webhookStackID(after), before, c.Kind, []webhooks.Event{event})
		return store.MetadataCommit{ID: id, Done: func(error) {}}, e
	})
	created, e := st.Create("recover", "// initial\n")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.Update("recover", "// next\n"); e != nil {
		t.Fatal(e)
	}
	if e = s.configureWebhookMetadata(ctx, s.webhooks); e != nil {
		t.Fatal(e)
	}
	if e = s.webhooks.ReconcileMetadata(ctx); e != nil {
		t.Fatal(e)
	}
	ds, _ := s.webhooks.Deliveries(ctx, ep.ID)
	if len(ds) != 2 {
		t.Fatalf("lost earlier committed mutation during next write: %+v", ds)
	}
	current, _ := st.Get("recover")
	if !current.CreatedAt.Equal(created.CreatedAt) {
		t.Fatal("recovery recreated the source registration")
	}
}
func TestSourceCapacityFailurePrecedesFileMutation(t *testing.T) {
	s, st, _, _ := dispatcherServer(t)
	moduleDir := filepath.Join(s.cfg.DataDir, "stacks", "example", "modules")
	if e := os.MkdirAll(moduleDir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(moduleDir, "extra.pkl"), []byte("nested unchanged fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := st.WriteFile("example", "extra.pkl", "unchanged fixture"); e != nil {
		t.Fatal(e)
	}
	before, e := st.ReadPkl("example")
	if e != nil {
		t.Fatal(e)
	}
	st.SetMetadataObserver(func(store.MetadataChange) (store.MetadataCommit, error) {
		return store.MetadataCommit{}, webhooks.ErrCapacity
	})
	if _, e = st.Update("example", "must not be saved\n"); !errors.Is(e, webhooks.ErrCapacity) {
		t.Fatal("missing capacity denial", e)
	}
	after, e := st.ReadPkl("example")
	if e != nil || after != before {
		t.Fatal("source changed before journal admission")
	}
	if _, e = st.Create("capacity-denied", "must not be created\n"); !errors.Is(e, webhooks.ErrCapacity) {
		t.Fatal(e)
	}
	if _, e = st.Get("capacity-denied"); !errors.Is(e, store.ErrStackNotFound) {
		t.Fatal("denied source registration appeared")
	}
	for _, mutate := range []func() error{func() error { return st.WriteFile("example", "extra.pkl", "must not change") }, func() error { return st.DeleteFile("example", "extra.pkl") }, func() error { return st.WriteLocalEditorFile("example", "modules/extra.pkl", "must not change") }, func() error { return st.Delete("example") }} {
		if e := mutate(); !errors.Is(e, webhooks.ErrCapacity) {
			t.Fatal("file mutation skipped journal admission", e)
		}
	}
	content, e := st.ReadFile("example", "extra.pkl")
	if e != nil || content != "unchanged fixture" {
		t.Fatal("capacity denial changed or removed source", e)
	}
	content, e = st.ReadLocalEditorFile("example", "modules/extra.pkl")
	if e != nil || content != "nested unchanged fixture" {
		t.Fatal("capacity denial truncated nested source", e)
	}
}
