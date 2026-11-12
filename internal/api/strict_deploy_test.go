//go:build unix

package api

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/secretbindings"
	"github.com/umayr/brine/internal/store"
)

func TestStrictManualDeployRejectsOldBinaryWithImport(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, ".brine-strict-api-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	workspace := filepath.Join(dir, "workspace")
	stateDir := filepath.Join(dir, "state")
	if err = os.Mkdir(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	st, err := store.NewFSStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := st.Create("demo", "import \"secret.pkl\"\n")
	if err != nil {
		t.Fatal(err)
	}
	state, err := secretbindings.Open(stateDir, workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	binary := filepath.Join(dir, "dill")
	marker := filepath.Join(dir, "up-called")
	script := "#!/bin/sh\ncase \" $* \" in *\" up \"*) echo called > '" + marker + "';; esac\nexit 1\n"
	if err = os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	runner := dill.New(binary)
	secret := &dill.SecretRunner{Runner: runner, State: state, Coordinator: &runner.Coordinator, Engine: "docker", Socket: "/tmp/synthetic.sock", Policy: "/tmp/synthetic-policy", Target: "lab-a"}
	runner.StrictActions = &dill.StrictController{Secret: secret, Resolve: func(path string) (string, error) {
		if path != stack.PklPath {
			t.Fatal("wrong path")
		}
		return "demo", nil
	}, LegacyGate: func(context.Context, string) error { return nil }}
	h := stackHandler{store: st, agent: agent.NewLocalAgent(nil, runner), deployTimeout: time.Second}
	req := httptest.NewRequest("POST", "/", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("name", "demo")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	w := httptest.NewRecorder()
	h.up(w, req)
	if !strings.Contains(w.Body.String(), "install a compatible Dill") {
		t.Fatalf("missing upgrade error: %s", w.Body.String())
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("old binary deploy executed")
	}
}
