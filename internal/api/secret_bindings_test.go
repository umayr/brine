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
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/secretbindings"
	"github.com/umayr/brine/internal/store"
)

// A configured strict controller must not reactivate the disabled API. Auth,
// CSRF and persistent classification remain effective across attempted calls.
func TestSecretBindingAPIDisabledPreservesAuthenticationAndState(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, ".brine-api-secret-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	workspace, private := filepath.Join(dir, "workspace"), filepath.Join(dir, "private")
	if err = os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	st, err := store.NewFSStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Create("demo", "x"); err != nil {
		t.Fatal(err)
	}
	state, err := secretbindings.Open(private, workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	id := secretbindings.DeploymentID("demo")
	if err = state.MarkSecretEnabled(id); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "called")
	bin := filepath.Join(dir, "dill")
	if err = os.WriteFile(bin, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runner := dill.New(bin)
	sr := &dill.SecretRunner{Runner: runner, State: state, Coordinator: &runner.Coordinator, Engine: "docker", Socket: "/tmp/synthetic.sock", Policy: "/tmp/policy", Target: "lab-a"}
	runner.StrictActions = &dill.StrictController{Secret: sr}
	ag := agent.NewLocalAgent(nil, runner)
	a, err := auth.New(auth.Config{User: "admin", Password: "password", APIToken: "synthetic-token"})
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(a.Middleware)
	r.Post("/login", a.HandleLogin)
	Mount(r, ag, st, nil, nil, "docker", httpx.Timeouts{}, time.Second)
	base := "/api/stacks/demo/secret-bindings"
	req := httptest.NewRequest("GET", base, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	login := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=password"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lw := httptest.NewRecorder()
	r.ServeHTTP(lw, login)
	cookies := lw.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login failed")
	}
	csrf := httptest.NewRequest("PUT", base, strings.NewReader("{}"))
	csrf.AddCookie(cookies[0])
	cw := httptest.NewRecorder()
	r.ServeHTTP(cw, csrf)
	if cw.Code != http.StatusForbidden {
		t.Fatalf("csrf: %d", cw.Code)
	}
	for _, tc := range []struct{ method, path string }{{"GET", base}, {"PUT", base}, {"POST", base + "/status"}, {"POST", base + "/preview"}, {"POST", base + "/confirm"}} {
		req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"value":"synthetic-secret-value"}`))
		req.Header.Set("Authorization", "Bearer synthetic-token")
		w = httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "disabled") || strings.Contains(w.Body.String(), "synthetic-secret-value") {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	if enabled, err := state.SecretEnabled(id); err != nil || !enabled {
		t.Fatalf("classification changed: %v %v", enabled, err)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("disabled endpoint ran subprocess: %v", err)
	}
}
