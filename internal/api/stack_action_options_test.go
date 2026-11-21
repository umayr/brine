package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
)

func TestStackActionsRejectInvalidOptionsWithoutMutation(t *testing.T) {
	for _, action := range []struct {
		method, path, option string
	}{
		{http.MethodDelete, "/api/stacks/demo", "remove_containers"},
		{http.MethodPost, "/api/stacks/demo/teardown", "force"},
	} {
		t.Run(action.option, func(t *testing.T) {
			router, st, sourceChecks := newStackActionTestRouter(t)
			const want = `{"error":"invalid request body"}`
			for _, tc := range []struct{ name, body string }{
				{"truncated", "{"},
				{"wrong option type", fmt.Sprintf(`{%q:"false"}`, action.option)},
				{"array", "[]"},
				{"null", "null"},
				{"second value", "{} {}"},
				{"trailing garbage", "{} trailing"},
				{"oversized trailing value", `{} "` + strings.Repeat("a", maxStackActionOptionsBytes) + `"`},
				{"oversized trailing whitespace", "{}" + strings.Repeat(" ", maxStackActionOptionsBytes)},
			} {
				t.Run(tc.name, func(t *testing.T) {
					w := httptest.NewRecorder()
					router.ServeHTTP(w, httptest.NewRequest(action.method, action.path, strings.NewReader(tc.body)))
					if w.Code != http.StatusBadRequest || strings.TrimSpace(w.Body.String()) != want {
						t.Errorf("response = %d %q, want 400 %s", w.Code, w.Body.String(), want)
					}
					if *sourceChecks != 0 {
						t.Errorf("unexpected Dill teardown attempts: %d", *sourceChecks)
					}
					if content, err := st.ReadPkl("demo"); err != nil || content != stackActionTestRecipe {
						t.Errorf("stack source changed: ReadPkl = %q, %v", content, err)
					}
				})
			}
		})
	}
}

func TestStackActionsAcceptOptionalOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		body func(option string) io.Reader
	}{
		{"omitted", func(string) io.Reader { return nil }},
		{"whitespace", func(string) io.Reader { return strings.NewReader(" \n\t\r") }},
		{"empty object", func(string) io.Reader { return strings.NewReader("{}") }},
		{"explicit false", func(option string) io.Reader {
			return strings.NewReader(fmt.Sprintf("{%q:false}\n", option))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router, st, sourceChecks := newStackActionTestRouter(t)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/stacks/demo/teardown", tc.body("force")))
			if w.Code != http.StatusOK || *sourceChecks != 1 {
				t.Errorf("teardown did not reach the synthetic source guard: status %d, source checks %d", w.Code, *sourceChecks)
			}

			w = httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/stacks/demo", tc.body("remove_containers")))
			if w.Code != http.StatusNoContent {
				t.Errorf("delete status = %d, want 204", w.Code)
			}
			if _, err := st.Get("demo"); !errors.Is(err, store.ErrStackNotFound) {
				t.Errorf("stack was not deleted: Get error = %v", err)
			}
		})
	}
}

const stackActionTestRecipe = "config { engine = \"docker\" }\n"

func newStackActionTestStore(t *testing.T) *store.FSStore {
	t.Helper()
	st, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Create("demo", stackActionTestRecipe); err != nil {
		t.Fatal(err)
	}
	return st
}

func newStackActionTestRouter(t *testing.T) (*chi.Mux, *store.FSStore, *int) {
	t.Helper()
	st := newStackActionTestStore(t)
	sourceChecks := 0
	runner := dill.New("/must-not-run")
	runner.CheckSource = func(string) error {
		sourceChecks++
		return errors.New("synthetic source guard prevents runtime execution")
	}
	router := chi.NewRouter()
	Mount(router, agent.NewLocalAgent(nil, runner), st, nil, nil, "docker", httpx.Timeouts{}, 0)
	return router, st, &sourceChecks
}
