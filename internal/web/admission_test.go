package web

import (
	"context"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/dill"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStackAdmissionCancellation(t *testing.T) {
	runner := dill.New("unused")
	h := Handler{agent: agent.NewLocalAgent(nil, runner)}
	unlock := runner.LockStack("demo")
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	done := make(chan bool, 1)
	go func() {
		release, ok := h.admitStack(w, r, "demo")
		if ok {
			release()
		}
		done <- ok
	}()
	cancel()
	select {
	case admitted := <-done:
		if admitted {
			t.Fatal("cancelled request admitted")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter retained")
	}
	if w.Code != http.StatusRequestTimeout {
		t.Fatalf("status %d", w.Code)
	}
}
