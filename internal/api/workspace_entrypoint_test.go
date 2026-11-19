package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
)

func TestStackReadEndpointsReportEffectiveEntrypoint(t *testing.T) {
	root := t.TempDir()
	st, err := store.NewFSStore(root)
	if err != nil {
		t.Fatal(err)
	}
	const legacyPkl = "// synthetic legacy entrypoint\n"
	legacy, err := st.Create("legacy", legacyPkl)
	if err != nil {
		t.Fatal(err)
	}
	legacyMetadataPath := filepath.Join(filepath.Dir(legacy.PklPath), "meta.json")
	// Older local stack metadata can omit the entrypoint; compose.pkl is still readable.
	legacyMetadata := []byte(`{"name":"legacy","pkl_path":"/old/workspace/compose.pkl"}`)
	if err := os.WriteFile(legacyMetadataPath, legacyMetadata, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Create("explicit", "// default\n"); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteFile("explicit", "chosen.pkl", "// chosen\n"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEntrypoint("explicit", "chosen.pkl"); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	Mount(router, nil, st, nil, nil, "docker", httpx.Timeouts{}, 0)
	get := func(path string, dst any) {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s returned %d, want 200", path, w.Code)
		}
		if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
			t.Fatal(err)
		}
	}
	var detail stackResponse
	get("/api/stacks/legacy", &detail)
	if detail.Entrypoint != "compose.pkl" || detail.PklPath != legacy.PklPath || detail.PklContent != legacyPkl {
		t.Errorf("legacy detail entrypoint = %q, path = %q, content = %q", detail.Entrypoint, detail.PklPath, detail.PklContent)
	}
	var listing []store.Stack
	get("/api/stacks/", &listing)
	entries := make(map[string]string)
	for _, stack := range listing {
		entries[stack.Name] = stack.Entrypoint
	}
	if entries["legacy"] != "compose.pkl" || entries["explicit"] != "chosen.pkl" {
		t.Errorf("listing entrypoints = %v", entries)
	}
	if got, err := os.ReadFile(legacyMetadataPath); err != nil || !bytes.Equal(got, legacyMetadata) {
		t.Fatal("reading metadata rewrote the legacy file")
	}
}
