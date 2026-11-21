//go:build unix

package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/httpx"
)

func TestStackActionsPreserveExplicitOptions(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "fake-dill")
	const script = `#!/bin/sh
shift 8
case "$*" in
    'teardown --force') echo 'synthetic forced teardown';;
    'down') echo 'synthetic down'; exit 1;;
    *) exit 2;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	runner := dill.New(bin)
	runner.Engine = "docker"
	runner.Socket = filepath.Join(t.TempDir(), "unused.sock")
	runner.ResolveStack = func(string) (string, error) { return "demo", nil }
	router := chi.NewRouter()
	Mount(router, agent.NewLocalAgent(nil, runner), newStackActionTestStore(t), nil, nil, "docker", httpx.Timeouts{}, 5*time.Second)
	for _, tc := range []struct {
		method, path, body, marker string
		status                     int
	}{
		{http.MethodPost, "/api/stacks/demo/teardown", `{"force":true}`, "synthetic forced teardown", http.StatusOK},
		{http.MethodDelete, "/api/stacks/demo", `{"remove_containers":true}`, "synthetic down", http.StatusBadGateway},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.marker) {
			t.Errorf("%s %s = %d %q, want %d with %q", tc.method, tc.path, w.Code, w.Body.String(), tc.status, tc.marker)
		}
	}
}
