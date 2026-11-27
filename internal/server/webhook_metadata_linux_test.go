//go:build linux

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/store"
)

// Unit-level synthetic revision IDs exercise the authoritative workspace commit
// hook, not archive encryption or a real browser approval. Existing archive/UI
// tests own those contracts; this test cannot deploy or restore runtime data.
func TestSourceRevisionAndRestoreEventsFollowAtomicPublication(t *testing.T) {
	ctx := context.Background()
	s, st, _, logPath := dispatcherServer(t)
	if e := s.configureWebhookMetadata(ctx, s.webhooks); e != nil {
		t.Fatal(e)
	}
	ep := metadataEndpoint(t, s)
	first, second := strings.Repeat("1", 64), strings.Repeat("2", 64)
	v, e := st.ReviewSource(ctx, "example")
	if e != nil {
		t.Fatal(e)
	}
	if e = st.CommitSource(ctx, "example", v.Baseline, func(store.SourceView) (store.SourceUpdate, error) {
		return store.SourceUpdate{NewRevisions: []string{first}}, nil
	}); e != nil {
		t.Fatal(e)
	}
	v, e = st.ReviewSource(ctx, "example")
	if e != nil {
		t.Fatal(e)
	}
	const restored = "// synthetic restored source must not leave webhook metadata\n"
	if e = st.CommitSource(ctx, "example", v.Baseline, func(store.SourceView) (store.SourceUpdate, error) {
		return store.SourceUpdate{NewRevisions: []string{second}, Entrypoint: "compose.pkl", Files: map[string][]byte{"compose.pkl": []byte(restored)}}, nil
	}); e != nil {
		t.Fatal(e)
	}
	source, e := st.ReadPkl("example")
	if e != nil || source != restored {
		t.Fatal("source publication failed", e)
	}
	if e = s.webhooks.ReconcileMetadata(ctx); e != nil {
		t.Fatal(e)
	}
	events, e := s.webhooks.Deliveries(ctx, ep.ID)
	if e != nil {
		t.Fatal(e)
	}
	counts := map[string]int{}
	for _, d := range events {
		counts[d.Event.Type]++
		payload, _ := json.Marshal(d.Event)
		if bytes.Contains(payload, []byte(restored)) {
			t.Fatal("source bytes leaked")
		}
		if d.Event.Type == "source.restored" && (d.Event.Source.ID != second || d.Event.Target.Kind != "none") {
			t.Fatal("restore inferred execution or wrong revision")
		}
	}
	if counts["source.revision_created"] != 2 || counts["source.restored"] != 1 || counts["stack.updated"] != 2 {
		t.Fatal("lost or duplicated source publication events", counts)
	}
	if _, e = os.Stat(logPath); !os.IsNotExist(e) {
		t.Fatal("source Restore invoked Dill")
	}
}
