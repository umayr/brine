package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/targetregistry"
	"github.com/umayr/brine/internal/webhooks"
)

func webhookRegistry(t *testing.T, s *Server) *targetregistry.Registry {
	t.Helper()
	dir := privateTestDir(t)
	if e := targetregistry.Init(dir); e != nil {
		t.Fatal(e)
	}
	r, e := targetregistry.OpenExisting(dir)
	if e != nil {
		t.Fatal(e)
	}
	s.targetRegistry = r
	return r
}
func targetEndpoint(t *testing.T, s *Server) webhooks.Endpoint {
	t.Helper()
	ep, _, e := s.webhooks.SaveEndpoint(context.Background(), webhooks.Endpoint{Name: "targets", URL: "https://hooks.example.test/targets", Enabled: true, Events: []string{"target.enrolled", "target.updated", "target.removed"}})
	if e != nil {
		t.Fatal(e)
	}
	return ep
}
func TestTargetRegistryEventsFollowCommittedReviewedChanges(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := dispatcherServer(t)
	r := webhookRegistry(t, s)
	if e := s.configureWebhookMetadata(ctx, s.webhooks); e != nil {
		t.Fatal(e)
	}
	ep := targetEndpoint(t, s)
	row := syntheticRecord()
	if e := r.Add(row, row); e != nil {
		t.Fatal(e)
	}
	next := row
	next.Revision++
	next.Host = "changed.example"
	if e := r.Update(row, next, targetregistry.ReviewedUpdate{Previous: row, Next: next}); e != nil {
		t.Fatal(e)
	}
	paired := targetregistry.Record{ID: "paired", Revision: 1, HTTPSAddress: "192.168.0.5:9443", HTTPSPairing: targetregistry.PairingSecret(fmt.Sprintf(`{"version":1,"certificateSha256":%q,"token":%q,"agentPublicKey":%q,"engineId":"11111111-1111-4111-8111-111111111111","generation":1,"engineKind":"docker","engineSocket":"unix:///run/docker.sock","daemonId":"synthetic"}`, strings.Repeat("11", 32), base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)), strings.Repeat("33", 32)))}
	if e := r.Add(paired, paired); e != nil {
		t.Fatal(e)
	}
	if e := r.ForgetHTTPS(ctx, paired.ID); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e := s.webhooks.ReconcileMetadata(ctx); e != nil {
			t.Fatal(e)
		}
	}
	ds, e := s.webhooks.Deliveries(ctx, ep.ID)
	if e != nil || len(ds) != 4 {
		t.Fatal("lost/duplicated registrations", len(ds), e)
	}
	counts := map[string]int{}
	for _, d := range ds {
		counts[d.Event.Type]++
		if d.Event.Stack.ID != "" || d.Event.Target.Kind != "registered-target" || d.Event.Reason != "registration_committed" {
			t.Fatal("registration inferred stack activity or connectivity")
		}
	}
	if counts["target.enrolled"] != 2 || counts["target.updated"] != 1 || counts["target.removed"] != 1 {
		t.Fatal(counts)
	}
	raw, _ := json.Marshal(ds)
	for _, private := range []string{"192.168.0.5", "changed.example", "/private/key", "certificateSha256", "https_pairing"} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatal("private target metadata exported", private)
		}
	}
}
func TestTargetCommitRecoveryDoesNotNeedCallbackOrReadableSource(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := dispatcherServer(t)
	r := webhookRegistry(t, s)
	if e := s.configureWebhookMetadata(ctx, s.webhooks); e != nil {
		t.Fatal(e)
	}
	ep := targetEndpoint(t, s)
	r.SetCommitObserver(func(ctx context.Context, before string, c []targetregistry.CommitChange) (targetregistry.CommitReceipt, error) {
		id, e := s.webhooks.BeginMetadataMutation(ctx, s.targetJournalScope, s.targetJournalScope, before, "registry", []webhooks.Event{{Type: "target.enrolled", Target: webhooks.Resource{ID: webhookRegisteredTargetID(s.targetJournalScope, c[0].After.ID)}}})
		return targetregistry.CommitReceipt{ID: id, Done: func(error) {}}, e
	})
	row := syntheticRecord()
	if e := r.Add(row, row); e != nil {
		t.Fatal(e)
	}
	// Registration recovery uses registry evidence, never a stack-name fallback.
	if e := os.RemoveAll(filepath.Join(s.cfg.DataDir, "stacks")); e != nil {
		t.Fatal(e)
	}
	if e := s.webhooks.ReconcileMetadata(ctx); e != nil {
		t.Fatal(e)
	}
	ds, _ := s.webhooks.Deliveries(ctx, ep.ID)
	if len(ds) != 1 || !ds[0].Event.Reconciled {
		t.Fatal("committed target lost without callback", ds)
	}
}
