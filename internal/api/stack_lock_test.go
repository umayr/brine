package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/store"
)

func TestStackUpdateSharesDeploymentLock(t *testing.T) {
	st, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Create("demo", "old"); err != nil {
		t.Fatal(err)
	}
	runner := dill.New("unused")
	h := stackHandler{store: st, agent: agent.NewLocalAgent(nil, runner)}
	unlock := runner.LockStack("demo")
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := httptest.NewRequest("PUT", "/", strings.NewReader(`{"pkl_content":"new"}`))
		route := chi.NewRouteContext()
		route.URLParams.Add("name", "demo")
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
		h.update(httptest.NewRecorder(), r)
	}()
	select {
	case <-done:
		t.Fatal("update bypassed lock")
	case <-time.After(30 * time.Millisecond):
	}
	before, err := st.ReadPkl("demo")
	if err != nil || before != "old" {
		t.Fatal("updated while locked")
	}
	unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("update remained blocked")
	}
	after, err := st.ReadPkl("demo")
	if err != nil || after != "new" {
		t.Fatal("update not applied")
	}
}
