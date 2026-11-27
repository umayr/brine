package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/umayr/brine/internal/webhooks"
)

func TestLostInventoryRootDoesNotFabricateStackDeletion(t *testing.T) {
	ctx := context.Background()
	s, st, _, _ := dispatcherServer(t)
	if e := s.configureWebhookMetadata(ctx, s.webhooks); e != nil {
		t.Fatal(e)
	}
	ep := metadataEndpoint(t, s)
	stack, _ := st.Get("example")
	sid := webhookStackID(stack)
	if _, e := s.webhooks.BeginMetadataMutation(ctx, "example", sid, stack.EventHead, "delete", []webhooks.Event{{Type: "stack.deleted", Stack: webhooks.Resource{ID: sid, Name: "example"}}}); e != nil {
		t.Fatal(e)
	}
	if e := os.RemoveAll(filepath.Join(s.cfg.DataDir, "stacks")); e != nil {
		t.Fatal(e)
	}
	if e := s.webhooks.ReconcileMetadata(ctx); e != nil {
		t.Fatal(e)
	}
	deliveries, e := s.webhooks.Deliveries(ctx, ep.ID)
	if e != nil || len(deliveries) != 0 {
		t.Fatal("missing inventory root became an authoritative deletion", e, deliveries)
	}
}
