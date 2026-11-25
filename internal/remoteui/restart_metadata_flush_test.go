package remoteui

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/targetregistry"
)

type heldRestartFlush struct {
	http.ResponseWriter
	entered chan struct{}
	release chan struct{}
}

func (w *heldRestartFlush) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *heldRestartFlush) FlushError() error {
	close(w.entered)
	<-w.release
	return http.NewResponseController(w.ResponseWriter).Flush()
}

// This holds the actual handler's flush at an injected sink boundary. It is
// not evidence of kernel socket-buffer exhaustion or HTTP/2 flow control.
func TestRestartMetadataLeaseCoversFlush(t *testing.T) {
	h, cookie := restartHTTPFixture(t)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	reader := restartReaderFunc(func(context.Context, snapshothistory.RestartDiscoveryRequest) (snapshothistory.RestartDiscoveryPage, error) {
		return snapshothistory.RestartDiscoveryPage{More: true, Next: "next"}, nil
	})
	policy := restartMetadataAccess(func(context.Context, archiveReadActor, targetregistry.Record, targetregistry.HistoryIncarnation) error {
		return nil
	})
	handler := h.protectedRestartMetadata(reader, policy)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		handler.ServeHTTP(&heldRestartFlush{w, entered, release}, r)
	}))
	defer server.Close()
	defer unblock()
	result := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest("GET", server.URL+"/?host=demo", nil)
		req.AddCookie(cookie)
		client := server.Client()
		client.Timeout = 5 * time.Second
		res, err := client.Do(req)
		if err == nil {
			_, err = io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("flush not reached")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err := h.registry.ForgetHTTPS(ctx, "demo")
	contextErr := ctx.Err()
	cancel()
	if err == nil || contextErr != context.DeadlineExceeded {
		t.Fatalf("forget crossed delivery lease: %v", err)
	}
	unblock()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("handler did not release")
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.registry.ForgetHTTPS(ctx, "demo"); err != nil {
		t.Fatalf("lease retained after delivery: %v", err)
	}
}
