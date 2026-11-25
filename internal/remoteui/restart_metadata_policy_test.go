package remoteui

import (
	"context"
	"testing"

	"github.com/umayr/brine/internal/targetregistry"
)

func TestRestartMetadataAllowlistPinsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	h, _ := restartHTTPFixture(t)
	record, id, lease, err := h.registry.AcquireHistoryRead(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	approvals := []restartMetadataApproval{{host: "demo", incarnation: id}}
	policy, err := newRestartMetadataAllowlist(ctx, h.registry, approvals)
	if err != nil {
		t.Fatal(err)
	}
	actor := archiveReadActor{operator: "admin", session: deploymentSession("generated")}
	if err := policy(ctx, actor, record, id); err != nil {
		t.Fatal(err)
	}
	if policy(ctx, archiveReadActor{}, record, id) == nil {
		t.Fatal("missing actor allowed")
	}
	altered := record
	altered.ID = "other"
	if policy(ctx, actor, altered, id) == nil {
		t.Fatal("other host allowed")
	}
	altered = record
	altered.HTTPSAddress = "127.0.0.1:2"
	if policy(ctx, actor, altered, id) == nil {
		t.Fatal("changed target allowed")
	}
	for _, bad := range [][]restartMetadataApproval{{{host: "demo"}}, {approvals[0], approvals[0]}, {approvals[0], {host: "missing", incarnation: id}}} {
		if got, err := newRestartMetadataAllowlist(ctx, h.registry, bad); err == nil || got != nil {
			t.Fatal("invalid/partial allowlist accepted")
		}
	}
	if err := h.registry.ForgetHTTPS(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if err := h.registry.AddContext(ctx, record, record); err != nil {
		t.Fatal(err)
	}
	replacement, newID, lease, err := h.registry.AcquireHistoryRead(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if id == newID || policy(ctx, actor, replacement, newID) == nil {
		t.Fatal("replacement inherited approval")
	}
	// Reconstructing the policy from unchanged startup settings is still denial.
	if got, err := newRestartMetadataAllowlist(ctx, h.registry, approvals); err == nil || got != nil {
		t.Fatal("restart renewed stale approval")
	}
	fresh, err := newRestartMetadataAllowlist(ctx, h.registry, []restartMetadataApproval{{host: "demo", incarnation: newID}})
	if err != nil || fresh(ctx, actor, replacement, newID) != nil {
		t.Fatal("explicit new incarnation refused")
	}
}

func TestRestartMetadataEmptyAllowlistNeverReadsRegistry(t *testing.T) {
	policy, err := newRestartMetadataAllowlist(nil, nil, nil)
	if err != nil || policy == nil {
		t.Fatal("disabled configuration unavailable")
	}
	if policy(context.Background(), archiveReadActor{operator: "admin", session: deploymentSession("generated")}, targetregistry.Record{}, targetregistry.HistoryIncarnation{}) == nil {
		t.Fatal("empty allowlist granted access")
	}
}
