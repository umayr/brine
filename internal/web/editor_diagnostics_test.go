package web

import (
	"context"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/store"
)

func TestEditorDraftValidationAndFormat(t *testing.T) {
	st, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Create("test", "saved"); err != nil {
		t.Fatal(err)
	}
	if err = st.WriteFile("test", "other.pkl", "other"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "args")
	binary := filepath.Join(dir, "dill")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + log + "'\ncat > '" + log + ".stdin'\ncase \" $* \" in *\" validate \"*) printf '%s' '{\"valid\":false,\"error\":\"bad <source>\\nline 2\"}';; *) echo formatted;; esac\n"
	if err = os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	runner := dill.New(binary)
	runner.Engine = "docker"
	runner.Socket = "/synthetic/engine.sock"
	runner.ResolveStack = func(string) (string, error) { return "test", nil }
	h := &Handler{store: st, agent: agent.NewLocalAgent(nil, runner)}
	request := func(values url.Values, format bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/", strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		route := chi.NewRouteContext()
		route.URLParams.Add("name", "test")
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
		w := httptest.NewRecorder()
		if format {
			h.stackFmt(w, r)
		} else {
			h.stackValidate(w, r)
		}
		return w
	}
	w := request(url.Values{"pkl_content": {""}, "active_file": {"other.pkl"}}, false)
	if !strings.Contains(w.Body.String(), "bad &lt;source&gt;\nline 2") {
		t.Fatalf("diagnostic: %s", w.Body.String())
	}
	args, _ := os.ReadFile(log)
	input, _ := os.ReadFile(log + ".stdin")
	if !strings.Contains(string(args), "other.pkl\n-p\ntest\n--engine\ndocker\n--engine-socket\n/synthetic/engine.sock\nvalidate\n--stdin") || len(input) != 0 {
		t.Fatalf("args %q input %q", args, input)
	}
	w = request(url.Values{"pkl_content": {"x"}, "active_file": {"../evil.pkl"}}, true)
	if w.Code != 422 {
		t.Fatalf("unsafe path status %d", w.Code)
	}
	w = request(url.Values{"pkl_content": {"x"}, "active_file": {"other.pkl"}}, true)
	if w.Code != 200 || w.Body.String() != "formatted\n" {
		t.Fatalf("format: %d %q", w.Code, w.Body.String())
	}
}
