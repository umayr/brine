package api

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/secretbindings"
	"github.com/umayr/brine/internal/store"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRestartedQuarantineBlocksLifecycleAndDelete(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	st, err := store.NewFSStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Create("demo", "old"); err != nil {
		t.Fatal(err)
	}
	q, err := secretbindings.NewQuarantine(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.Begin(secretbindings.DeploymentID("demo")); err != nil {
		t.Fatal(err)
	}
	reopened, err := secretbindings.OpenQuarantine(root)
	if err != nil {
		t.Fatal(err)
	}
	runner := dill.New("must-not-execute")
	runner.ConfigureQuarantine(reopened)
	h := stackHandler{store: st, agent: agent.NewLocalAgent(nil, runner), deployTimeout: time.Second}
	route := chi.NewRouteContext()
	route.URLParams.Add("name", "demo")
	request := httptest.NewRequest("POST", "/", nil).WithContext(context.WithValue(context.Background(), chi.RouteCtxKey, route))
	calls := 0
	w := httptest.NewRecorder()
	h.lifecycle(w, request, "up", false, func(context.Context, string, io.Writer) error { calls++; return nil })
	if calls != 0 || w.Code < 400 || !strings.Contains(w.Body.String(), "quarantin") {
		t.Fatalf("lifecycle: calls=%d status=%d body=%s", calls, w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.delete(w, request)
	if w.Code < 400 || !strings.Contains(w.Body.String(), "quarantin") {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if _, err = st.Get("demo"); err != nil {
		t.Fatal("delete changed stack", err)
	}
	if reopened.Check(secretbindings.DeploymentID("demo")) == nil {
		t.Fatal("handler lost intent")
	}
}
