package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/configvars"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/planstate"
	"github.com/umayr/brine/internal/store"
)

func TestReviewedInputsCheckedBeforeExecution(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := st.Create("demo", "config {}")
	if err != nil {
		t.Fatal(err)
	}
	vars, err := configvars.New(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	run := dill.New("not-executed")
	run.Variables = vars
	h := stackHandler{store: st, agent: agent.NewLocalAgent(nil, run), deployTimeout: time.Second}
	var invoked atomic.Bool
	router := chi.NewRouter()
	router.Post("/stacks/{name}/up", func(w http.ResponseWriter, r *http.Request) {
		h.lifecycle(w, r, "up", false, func(context.Context, string, io.Writer) error { invoked.Store(true); return nil })
	})
	srv := httptest.NewServer(router)
	defer srv.Close()
	attempt := func(tag string, status int) {
		t.Helper()
		invoked.Store(false)
		r, e := http.NewRequest("POST", srv.URL+"/stacks/demo/up", nil)
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set(planstate.Header, tag)
		res, e := srv.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != status || invoked.Load() != (status == 200) {
			t.Fatalf("status %d; executed %v", res.StatusCode, invoked.Load())
		}
		if status == 412 && res.Header.Get("X-Brine-Plan-Stale") != "true" {
			t.Fatal("precondition not identified")
		}
	}
	tag, err := planstate.Tag(s, run)
	if err != nil {
		t.Fatal(err)
	}
	attempt(tag, 200)
	if err := vars.Set("", "APP_DOMAIN", "synthetic.test", false); err != nil {
		t.Fatal(err)
	}
	attempt(tag, 412)
	tag, err = planstate.Tag(s, run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Update("demo", "// edited"); err != nil {
		t.Fatal(err)
	}
	attempt(tag, 412)
}
func TestAPIStackMetadataOmitsPrivateLineage(t *testing.T) {
	st := &store.Stack{Name: "demo", Entrypoint: "compose.pkl", SourceHistory: &store.SourceLineage{Identity: "private"}}
	if stackReadMetadata(st).SourceHistory != nil || st.SourceHistory == nil {
		t.Fatal("history exposed or stored metadata modified")
	}
}
