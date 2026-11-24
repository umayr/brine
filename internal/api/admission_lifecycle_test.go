package api

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/store"
	"io"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAdmissionHonorsEarlierRequestDeadline(t *testing.T) {
	runner := dill.New("unused")
	h := stackHandler{agent: agent.NewLocalAgent(nil, runner)}
	unlock := runner.LockStack("demo")
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan bool, 1)
	go func() {
		release, ok := h.admitStack(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil).WithContext(ctx), "demo")
		if ok {
			release()
		}
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("expired waiter admitted")
		}
	case <-time.After(time.Second):
		t.Fatal("request deadline ignored")
	}
}

func TestAdmittedLifecycleSurvivesRequestCancellation(t *testing.T) {
	st, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Create("demo", "old"); err != nil {
		t.Fatal(err)
	}
	h := stackHandler{store: st, agent: agent.NewLocalAgent(nil, dill.New("unused")), deployTimeout: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	route := chi.NewRouteContext()
	route.URLParams.Add("name", "demo")
	r := httptest.NewRequest("POST", "/", nil).WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
	called := false
	h.lifecycle(httptest.NewRecorder(), r, "up", false, func(exec context.Context, _ string, _ io.Writer) error {
		called = true
		cancel()
		if exec.Err() != nil {
			t.Fatal("admitted execution inherited browser cancellation")
		}
		if _, ok := exec.Deadline(); !ok {
			t.Fatal("execution lost deadline")
		}
		return nil
	})
	if !called {
		t.Fatal("operation was not admitted")
	}
}
