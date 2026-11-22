//go:build linux || darwin

package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/api"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/web"
)

func TestGitCreateRoutes(t *testing.T) {
	for _, route := range []string{"api", "web"} {
		for _, scenario := range []string{"slow-success", "cancel", "expiry", "conflict"} {
			t.Run(route+"/"+scenario, func(t *testing.T) {
				root := t.TempDir()
				st, err := store.NewFSStore(root)
				if err != nil {
					t.Fatal(err)
				}
				bin := filepath.Join(t.TempDir(), "fake-git")
				script := "#!/bin/sh\nfor last do :; done\nmkdir -p \"$last/nested\"\nprintf ok > \"$last/nested/custom.pkl\"\nsleep 0.2\n"
				if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				if scenario == "conflict" {
					if err := os.Mkdir(filepath.Join(root, "stacks", "demo"), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, "stacks", "demo", "keep"), []byte("safe"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				timeout := time.Second
				if scenario == "expiry" {
					timeout = 50 * time.Millisecond
				}
				router := chi.NewRouter()
				runner := &git.Runner{Binary: bin}
				if route == "api" {
					api.Mount(router, nil, st, nil, runner, "docker", httpx.Timeouts{Default: 20 * time.Millisecond}, timeout)
				} else {
					web.Mount(router, web.New(nil, st, nil, runner, "docker", false, nil, timeout), httpx.Timeouts{Default: 20 * time.Millisecond})
				}
				var req *http.Request
				if route == "api" {
					req = httptest.NewRequest("POST", "/api/stacks/git", strings.NewReader(`{"name":"demo","git":{"url":"synthetic","folder":"nested"},"entrypoint":"custom.pkl"}`))
				} else {
					form := url.Values{"name": {"demo"}, "git_url": {"synthetic"}, "git_folder": {"nested"}, "git_entrypoint": {"custom.pkl"}}
					req = httptest.NewRequest("POST", "/web/stacks/git", strings.NewReader(form.Encode()))
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				}
				if scenario == "cancel" {
					ctx, cancel := context.WithCancel(req.Context())
					defer cancel()
					req = req.WithContext(ctx)
					time.AfterFunc(50*time.Millisecond, cancel)
				}
				w := httptest.NewRecorder()
				start := time.Now()
				router.ServeHTTP(w, req)
				if scenario == "slow-success" {
					if time.Since(start) < 100*time.Millisecond {
						t.Fatal("fake clone did not exceed ordinary timeout")
					}
					if route == "api" && w.Code != http.StatusCreated {
						t.Fatalf("status %d: %s", w.Code, w.Body.String())
					}
					if route == "web" && w.Header().Get("HX-Redirect") != "/stacks/demo" {
						t.Fatalf("redirect %q body %s", w.Header().Get("HX-Redirect"), w.Body.String())
					}
					stack, err := st.Get("demo")
					if err != nil {
						t.Fatal(err)
					}
					want := filepath.Join(root, "stacks", "demo", "repo", "nested", "custom.pkl")
					if stack.PklPath != want {
						t.Fatalf("path %q want %q", stack.PklPath, want)
					}
				} else {
					if route == "api" && w.Code == http.StatusCreated {
						t.Fatal("unexpected success")
					}
					if route == "web" && w.Header().Get("HX-Redirect") != "" {
						t.Fatal("unexpected redirect")
					}
					if scenario == "conflict" {
						b, err := os.ReadFile(filepath.Join(root, "stacks", "demo", "keep"))
						if err != nil || string(b) != "safe" {
							t.Fatalf("existing data changed: %q %v", b, err)
						}
					} else if _, err := os.Lstat(filepath.Join(root, "stacks", "demo")); !os.IsNotExist(err) {
						t.Fatalf("final stack residue: %v", err)
					}
				}
				entries, err := os.ReadDir(filepath.Join(root, "stacks"))
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range entries {
					if e.Name() != "demo" {
						t.Fatalf("staging residue: %s", e.Name())
					}
				}
			})
		}
	}
}

func TestGitCreateWaitsOnRegularStackLock(t *testing.T) {
	for _, route := range []string{"api", "web"} {
		for _, mode := range []string{"release", "cancel", "deadline"} {
			t.Run(route+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				st, err := store.NewFSStore(root)
				if err != nil {
					t.Fatal(err)
				}
				marker := filepath.Join(t.TempDir(), "started")
				bin := filepath.Join(t.TempDir(), "git")
				script := "#!/bin/sh\necho started > '" + marker + "'\nfor last do :; done\nmkdir -p \"$last\"\necho ok > \"$last/compose.pkl\"\n"
				if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				dr := dill.New("unused")
				unlock := dr.LockStack("demo")
				held := true
				defer func() {
					if held {
						unlock()
					}
				}()
				ag := agent.NewLocalAgent(nil, dr)
				gr := &git.Runner{Binary: bin}
				timeout := time.Second
				if mode == "deadline" {
					timeout = 80 * time.Millisecond
				}
				router := chi.NewRouter()
				if route == "api" {
					api.Mount(router, ag, st, nil, gr, "docker", httpx.Timeouts{}, timeout)
				} else {
					web.Mount(router, web.New(ag, st, nil, gr, "docker", false, nil, timeout), httpx.Timeouts{})
				}
				var req *http.Request
				if route == "api" {
					req = httptest.NewRequest("POST", "/api/stacks/git", strings.NewReader(`{"name":"demo","git":{"url":"fake"}}`))
				} else {
					req = httptest.NewRequest("POST", "/web/stacks/git", strings.NewReader("name=demo&git_url=fake"))
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				}
				ctx, cancel := context.WithCancel(req.Context())
				defer cancel()
				req = req.WithContext(ctx)
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { w := httptest.NewRecorder(); router.ServeHTTP(w, req); done <- w }()
				time.Sleep(30 * time.Millisecond)
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("clone ran while regular lock held")
				}
				if mode == "release" {
					unlock()
					held = false
				} else if mode == "cancel" {
					cancel()
				}
				select {
				case w := <-done:
					if mode == "release" {
						if _, err := st.Get("demo"); err != nil {
							t.Fatalf("registration failed %d %s: %v", w.Code, w.Body.String(), err)
						}
					} else {
						if _, err := os.Stat(marker); !os.IsNotExist(err) {
							t.Fatal("clone started while waiting")
						}
						if _, err := st.Get("demo"); err == nil {
							t.Fatal("registered while waiting")
						}
					}
				case <-time.After(2 * time.Second):
					t.Fatal("lock wait did not terminate")
				}
			})
		}
	}
}
