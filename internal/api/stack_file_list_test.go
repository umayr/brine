package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
)

func TestStackFileListUnavailableDiagnostic(t *testing.T) {
	st, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"healthy", "unavailable"} {
		stack, err := st.Create(name, "// synthetic\n")
		if err != nil {
			t.Fatal(err)
		}
		if name == "unavailable" {
			meta := filepath.Join(filepath.Dir(stack.PklPath), "meta.json")
			if err := os.WriteFile(meta, []byte(`{"git":{"folder":"/SYNTHETIC_PRIVATE_WORKSPACE"}}`), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	r := chi.NewRouter()
	Mount(r, nil, st, nil, nil, "docker", httpx.Timeouts{}, 0)
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"healthy", http.StatusOK, `["compose.pkl"]`},
		{"unavailable", http.StatusNotFound, `{"error":"Stack file list unavailable; check the stack name and local workspace storage"}`},
		{"missing", http.StatusNotFound, `{"error":"Stack file list unavailable; check the stack name and local workspace storage"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/stacks/"+tc.name+"/files", nil))
			if w.Code != tc.status {
				t.Errorf("status = %d, want %d", w.Code, tc.status)
			}
			if strings.TrimSpace(w.Body.String()) != tc.body {
				t.Error("response did not match the expected file list or safe diagnostic")
			}
		})
	}
}
