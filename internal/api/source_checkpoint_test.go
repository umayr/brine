package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
)

func TestSourceSaveObserversKeepSavedEditsAndReportMissedCheckpoint(t *testing.T) {
	st, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Create("demo", "value = 1"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, body string
		code               int
		check              func()
	}{
		{"PUT", "/api/stacks/demo", "{\"pkl_content\":\"value = 2\"}", 200, func() {
			if s, e := st.ReadPkl("demo"); e != nil || s != "value = 2" {
				t.Fatalf("saved content: %q, %v", s, e)
			}
		}},
		{"PUT", "/api/stacks/demo/files/other.pkl", "{\"content\":\"value = 3\"}", 204, func() {
			if s, e := st.ReadFile("demo", "other.pkl"); e != nil || s != "value = 3" {
				t.Fatalf("added content: %q, %v", s, e)
			}
		}},
		{"POST", "/api/stacks/demo/entrypoint", "{\"filename\":\"other.pkl\"}", 204, func() {
			if s, e := st.Get("demo"); e != nil || s.Entrypoint != "other.pkl" {
				t.Fatalf("new entrypoint lost: %v", e)
			}
		}},
		{"POST", "/api/stacks/demo/entrypoint", "{\"filename\":\"compose.pkl\"}", 204, func() {
			if s, e := st.Get("demo"); e != nil || s.Entrypoint != "compose.pkl" {
				t.Fatalf("entrypoint revert lost: %v", e)
			}
		}},
		{"DELETE", "/api/stacks/demo/files/other.pkl", "", 204, func() {
			if _, e := st.ReadFile("demo", "other.pkl"); e == nil {
				t.Fatal("deleted file still present")
			}
		}},
		{"PUT", "/api/stacks/demo/files/bad.txt", "{\"content\":\"value = 3\"}", 400, nil},
	} {
		called, released := false, false
		r := chi.NewRouter()
		MountObservingSourceSaves(r, nil, st, nil, nil, "docker", httpx.Timeouts{}, 0, func(*http.Request) (func(string) bool, func()) {
			return func(name string) bool {
				if name != "demo" || released || called || tc.check == nil {
					t.Fatal("unexpected checkpoint invocation")
				}
				called = true
				tc.check()
				return true
			}, func() { released = true }
		})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if w.Code != tc.code || called != (tc.check != nil) || !released || (w.Header().Get("X-Brine-Source-Checkpoint") == "not-retained") != called {
			t.Fatalf("%s %s status=%d expected=%d called=%v released=%v headers=%v", tc.method, tc.path, w.Code, tc.code, called, released, w.Header())
		}
	}
}
