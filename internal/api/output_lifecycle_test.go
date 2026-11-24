package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/store"
)

type stalledObserver struct {
	header   http.Header
	entered  chan struct{}
	once     sync.Once
	deadline time.Time
}

func (w *stalledObserver) Header() http.Header                { return w.header }
func (w *stalledObserver) WriteHeader(int)                    {}
func (w *stalledObserver) SetWriteDeadline(d time.Time) error { w.deadline = d; return nil }
func (w *stalledObserver) Flush()                             {}
func (w *stalledObserver) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	time.Sleep(max(0, time.Until(w.deadline)))
	return 0, os.ErrDeadlineExceeded
}
func TestLifecycleCompletesDespiteStalledOutput(t *testing.T) {
	st, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Create("demo", "old"); err != nil {
		t.Fatal(err)
	}
	runner := dill.New("unused")
	h := stackHandler{store: st, agent: agent.NewLocalAgent(nil, runner), deployTimeout: time.Second}
	route := chi.NewRouteContext()
	route.URLParams.Add("name", "demo")
	r := httptest.NewRequest("POST", "/", nil).WithContext(context.WithValue(context.Background(), chi.RouteCtxKey, route))
	w := &stalledObserver{header: make(http.Header), entered: make(chan struct{})}
	effect := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.lifecycle(w, r, "stop", false, func(ctx context.Context, _ string, out io.Writer) error {
			out.Write([]byte("progress"))
			select {
			case <-w.entered:
			case <-ctx.Done():
				return ctx.Err()
			}
			close(effect)
			return nil
		})
	}()
	select {
	case <-effect:
	case <-time.After(time.Second):
		t.Fatal("operation blocked on observer")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("observer failed to join")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := runner.LockStackContext(ctx, "demo")
	if err != nil {
		t.Fatal("stack admission retained after delivery timeout")
	}
	release()
}
