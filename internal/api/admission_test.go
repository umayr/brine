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
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/store"
)

func TestCancelledQueuedLifecycleDoesNotExecute(t *testing.T) {
	st, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Create("demo", "old"); err != nil {
		t.Fatal(err)
	}
	runner := dill.New("unused")
	h := stackHandler{store: st, agent: agent.NewLocalAgent(nil, runner), deployTimeout: time.Second}
	unlock := runner.LockStack("demo")
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	route := chi.NewRouteContext()
	route.URLParams.Add("name", "demo")
	r := httptest.NewRequest("POST", "/", nil).WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
	w := httptest.NewRecorder()
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.lifecycle(w, r, "up", false, func(context.Context, string, io.Writer) error { calls.Add(1); return nil })
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled request still waits behind stack lock")
	}
	if calls.Load() != 0 || w.Code != http.StatusRequestTimeout {
		t.Fatalf("calls=%d status=%d", calls.Load(), w.Code)
	}
	if w.Header().Get("Content-Type") != "application/json" {
		t.Fatal("API admission error is not JSON")
	}
}
