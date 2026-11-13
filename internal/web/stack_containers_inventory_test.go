package web

import (
	"errors"
	"fmt"
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
	"github.com/umayr/brine/internal/store"
)

const inventoryDiagnostic = "synthetic-sensitive-diagnostic"

// All commands in this suite are temporary shell fixtures, never real Dill or engines.
func stackInventoryFixture(t *testing.T) (*store.FSStore, *dill.Runner, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := st.Create("demo", "config { name = \"demo\" }")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "fake-dill")
	runner := dill.New(binary)
	runner.Engine = "docker"
	runner.Socket = filepath.Join(dir, "synthetic.sock")
	runner.ResolveStack = func(path string) (string, error) {
		if path != stack.PklPath {
			return "", errors.New(inventoryDiagnostic)
		}
		return "demo", nil
	}
	runner.CheckSource = func(string) error {
		_, err := st.ReadPkl("demo")
		return err
	}
	return st, runner, binary
}

func inventoryScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
}

func stackInventoryRoute(st store.StackStore, runner *dill.Runner) http.Handler {
	r := chi.NewRouter()
	Mount(r, &Handler{store: st, agent: agent.NewLocalAgent(nil, runner)}, httpx.Timeouts{Default: 3 * time.Second})
	return r
}

func requestStackInventory(t *testing.T, route http.Handler) string {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/web/stacks/demo/containers", nil)
	r.Header.Set("HX-Request", "true")
	route.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("expected swappable HTML partial: %d %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

func assertInventoryUnavailable(t *testing.T, body string) {
	t.Helper()
	for _, want := range []string{"role=\"alert\"", "Stack container inventory is unavailable.", "Retry", "hx-get=\"/web/stacks/demo/containers\"", "hx-target=\"#stack-containers\"", "hx-sync=\"#stack-containers:replace\"", "hx-disabled-elt=\"this\"", "10000", "aria-label=\"Retry stack container inventory\""} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %s", want, body)
		}
	}
	for _, forbidden := range []string{"No containers in this stack.", inventoryDiagnostic, "<table", "synthetic.sock", "compose.pkl", "fake-dill"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("unavailable inventory contains %q: %s", forbidden, body)
		}
	}
}

func TestStackContainersInventoryCommandStates(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		t.Run(engine, func(t *testing.T) {
			for _, tc := range []struct {
				name, script, want string
			}{
				{"empty", "printf '[]'", "No containers in this stack."},
				{"populated", `printf '[{"id":"abc123","name":"web","state":"running","status":"Up","image":"example:test","ports":["8080:80"]}]'`, "web"},
				{"socket unavailable", "echo 'cannot connect " + inventoryDiagnostic + "' >&2; exit 2", ""},
				{"unsupported command", "echo 'unknown --engine-socket " + inventoryDiagnostic + "' >&2; exit 1", ""},
				{"malformed JSON", "printf '" + inventoryDiagnostic + "'", ""},
				{"wrong JSON shape", "printf '{}'", ""},
				{"null is not an empty list", "printf 'null'", ""},
				{"blank output", "exit 0", ""},
				{"source evaluation failure", "echo 'source " + inventoryDiagnostic + " failed'; exit 2", ""},
				{"binary missing", "", ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					st, runner, binary := stackInventoryFixture(t)
					runner.Engine = engine
					if tc.script != "" {
						inventoryScript(t, binary, tc.script)
					}
					body := requestStackInventory(t, stackInventoryRoute(st, runner))
					if tc.want == "" {
						assertInventoryUnavailable(t, body)
						return
					}
					if !strings.Contains(body, tc.want) || strings.Contains(body, "unavailable") {
						t.Fatalf("expected successful %s: %s", tc.name, body)
					}
					if tc.name == "populated" {
						for _, want := range []string{"<table", "abc123", "running", "8080:80", ">Stop</button>"} {
							if !strings.Contains(body, want) {
								t.Errorf("missing %q: %s", want, body)
							}
						}
						if strings.Contains(body, "No containers in this stack.") {
							t.Fatal("populated inventory displayed empty state")
						}
					}
				})
			}
		})
	}
}

func TestStackContainersInventoryWorkspaceFailuresAndRecovery(t *testing.T) {
	for _, kind := range []string{"missing metadata", "corrupt metadata", "unsafe workspace", "missing source", "unsafe source", "source check rejected"} {
		t.Run(kind, func(t *testing.T) {
			st, runner, binary := stackInventoryFixture(t)
			marker := filepath.Join(t.TempDir(), "invoked")
			inventoryScript(t, binary, fmt.Sprintf("touch %q; printf '[]'", marker))
			stack, err := st.Get("demo")
			if err != nil {
				t.Fatal(err)
			}
			var restore func() error
			switch kind {
			case "missing metadata", "corrupt metadata":
				meta := filepath.Join(filepath.Dir(stack.PklPath), "meta.json")
				original, err := os.ReadFile(meta)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "missing metadata" {
					err = os.Rename(meta, meta+".saved")
				} else {
					err = os.WriteFile(meta, []byte(inventoryDiagnostic), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				restore = func() error { return os.WriteFile(meta, original, 0600) }
			case "unsafe workspace", "unsafe source":
				path := filepath.Dir(stack.PklPath)
				if kind == "unsafe source" {
					path = stack.PklPath
				}
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".saved", path); err != nil {
					t.Fatal(err)
				}
				// Rename replaces the test symlink without following it.
				restore = func() error { return os.Rename(path+".saved", path) }
				if kind == "unsafe workspace" {
					// A directory cannot replace a symlink atomically; retain the
					// unsafe fixture and restore metadata to a fresh safe workspace.
					restore = func() error {
						if err := os.Rename(path, path+".link"); err != nil {
							return err
						}
						return os.Rename(path+".saved", path)
					}
				}
			case "missing source":
				if err := os.Rename(stack.PklPath, stack.PklPath+".saved"); err != nil {
					t.Fatal(err)
				}
				restore = func() error { return os.Rename(stack.PklPath+".saved", stack.PklPath) }
			case "source check rejected":
				runner.CheckSource = func(string) error { return errors.New(inventoryDiagnostic) }
				restore = func() error { runner.CheckSource = nil; return nil }
			}
			route := stackInventoryRoute(st, runner)
			assertInventoryUnavailable(t, requestStackInventory(t, route))
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("Dill must not run with unavailable metadata/source: %v", err)
			}
			if err := restore(); err != nil {
				t.Fatal(err)
			}
			body := requestStackInventory(t, route)
			if !strings.Contains(body, "No containers in this stack.") || strings.Contains(body, "unavailable") {
				t.Fatalf("inventory did not recover: %s", body)
			}
		})
	}
}

func TestStackContainersInventoryCommandFailureRecovers(t *testing.T) {
	st, runner, binary := stackInventoryFixture(t)
	route := stackInventoryRoute(st, runner)
	for _, script := range []string{"exit 2", "printf '[]'", "printf 'invalid'", `printf '[{"id":"recovered123","name":"recovered","state":"running"}]'`} {
		inventoryScript(t, binary, script)
		body := requestStackInventory(t, route)
		if strings.Contains(script, "exit") || strings.Contains(script, "invalid") {
			assertInventoryUnavailable(t, body)
		} else if strings.Contains(body, "unavailable") || (!strings.Contains(body, "recovered") && !strings.Contains(body, "No containers in this stack.")) {
			t.Fatalf("inventory did not recover: %s", body)
		}
	}
}
