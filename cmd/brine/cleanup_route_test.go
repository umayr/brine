package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/api"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/web"
)

type recipeReadCounter struct {
	store.StackStore
	reads int
}

func (s *recipeReadCounter) ReadPkl(name string) (string, error) {
	s.reads++
	return s.StackStore.ReadPkl(name)
}

func TestCleanupRoutesWithUnavailableOrMismatchedRecipe(t *testing.T) {
	for _, kind := range []string{"missing", "symlink", "engine-mismatch"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			fs, err := store.NewFSStore(filepath.Join(root, "data"))
			if err != nil {
				t.Fatal(err)
			}
			stack, err := fs.Create("demo", `config { engine = "podman" }`)
			if err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(root, "external.pkl")
			if err = os.WriteFile(outside, []byte("synthetic-protected"), 0600); err != nil {
				t.Fatal(err)
			}
			if kind != "engine-mismatch" {
				if err = os.Remove(stack.PklPath); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					if err = os.Symlink(outside, stack.PklPath); err != nil {
						t.Fatal(err)
					}
				}
			}
			log := filepath.Join(root, "commands")
			binary := filepath.Join(root, "dill")
			script := "#!/bin/sh\n[ \"$1\" = -f ] && [ \"$2\" = \"$HOME/unavailable-config.pkl\" ] && [ ! -e \"$2\" ] || exit 8\ncase \" $* \" in *\" -p demo --engine docker --engine-socket /synthetic/engine.sock \"*) ;; *) exit 9;; esac\nprintf '%s\\n' \"$*\" >> '" + log + "'\n"
			if err = os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			st := &recipeReadCounter{StackStore: fs}
			runner := dill.New(binary)
			runner.Engine = "docker"
			runner.Socket = "/synthetic/engine.sock"
			runner.ResolveStack = stackPathResolver(st)
			runner.CheckSource = stackSourceCheck(st)
			ag := agent.NewLocalAgent(nil, runner)
			router := chi.NewRouter()
			authn, err := auth.New(auth.Config{User: "admin", Password: "synthetic-password", APIToken: "synthetic-token"})
			if err != nil {
				t.Fatal(err)
			}
			router.Use(authn.Middleware)
			api.Mount(router, ag, st, nil, nil, "docker", httpx.Timeouts{}, time.Second)
			web.Mount(router, web.New(ag, st, nil, nil, "docker", false, authn, time.Second), httpx.Timeouts{})
			request := func(method, path string, body *bytes.Buffer) *http.Request {
				if body == nil {
					body = &bytes.Buffer{}
				}
				req := httptest.NewRequest(method, path, body)
				req.Header.Set("Authorization", "Bearer synthetic-token")
				return req
			}
			page := httptest.NewRecorder()
			router.ServeHTTP(page, request(http.MethodGet, "/stacks/demo", nil))
			if page.Code != 200 {
				t.Fatalf("stack detail status: %d", page.Code)
			}
			html := page.Body.String()
			for _, action := range []string{"stop", "down"} {
				needle := "runAction('" + action + "', 'demo')"
				index := strings.Index(html, needle)
				if index < 0 {
					t.Fatalf("web missing %s action", action)
				}
				start := strings.LastIndex(html[:index], "<button ")
				end := strings.Index(html[index:], "</button>")
				if start < 0 || end < 0 {
					t.Fatalf("missing %s button", action)
				}
				if strings.Contains(html[start:index+end], "disabled") {
					t.Fatalf("web %s disabled", action)
				}
			}
			if !strings.Contains(html, "fetch(`/api/stacks/${name}/${action}`") {
				t.Fatal("web actions no longer use lifecycle API")
			}
			for _, action := range []string{"stop", "down"} {
				st.reads = 0
				w := httptest.NewRecorder()
				router.ServeHTTP(w, request(http.MethodPost, "/api/stacks/demo/"+action, nil))
				if w.Code != 200 || !strings.Contains(w.Body.String(), "[brine:done] "+action+" complete") {
					t.Fatalf("%s route: %d %s", action, w.Code, w.Body.String())
				}
				if st.reads != 0 {
					t.Fatalf("%s read recipe", action)
				}
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) != 2 {
				t.Fatalf("wrong command count: %d", len(lines))
			}
			for _, line := range lines {
				args := strings.Fields(line)
				if len(args) < 2 || strings.Contains(line, stack.PklPath) {
					t.Fatal("cleanup used workspace recipe")
				}
				if _, err = os.Stat(filepath.Dir(args[1])); !os.IsNotExist(err) {
					t.Fatal("private cleanup directory not removed")
				}
			}
			for _, action := range []string{"up", "start", "restart", "plan"} {
				method := http.MethodPost
				if action == "plan" {
					method = http.MethodGet
				}
				w := httptest.NewRecorder()
				router.ServeHTTP(w, request(method, "/api/stacks/demo/"+action, nil))
				expected := http.StatusInternalServerError
				if kind == "engine-mismatch" {
					expected = http.StatusConflict
				}
				if w.Code != expected {
					t.Fatalf("%s source gate: %d %s", action, w.Code, w.Body.String())
				}
			}
			before, _ := os.ReadFile(log)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, request(http.MethodPost, "/api/stacks/demo/teardown", bytes.NewBufferString(`{}`)))
			if kind == "engine-mismatch" {
				if w.Code != http.StatusConflict {
					t.Fatalf("teardown engine gate: %d %s", w.Code, w.Body.String())
				}
			} else if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "teardown requires a safe recipe; stop and down remain available") {
				t.Fatalf("teardown guidance: %d %s", w.Code, w.Body.String())
			}
			after, _ := os.ReadFile(log)
			if !bytes.Equal(before, after) {
				t.Fatal("unsafe action invoked Dill")
			}
			external, err := os.ReadFile(outside)
			if err != nil || string(external) != "synthetic-protected" {
				t.Fatal("external file changed")
			}
			info, err := os.Stat(outside)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("external permissions changed")
			}
		})
	}
}
