package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func waitMetadataReady(t *testing.T, w *metadataWorker) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		ready := w.handler != nil
		w.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("metadata worker not ready")
}

func TestMetadataWorkerDisabledAndFailedOpen(t *testing.T) {
	if w := beginMetadataWorker(context.Background(), false, func(context.Context) (metadataEndpoint, error) {
		t.Fatal("disabled opener called")
		return metadataEndpoint{}, nil
	}); w != nil {
		t.Fatal("disabled worker allocated")
	}
	var closed atomic.Int32
	w := beginMetadataWorker(context.Background(), true, func(context.Context) (metadataEndpoint, error) {
		return metadataEndpoint{close: func() error { closed.Add(1); return nil }}, errors.New("private path")
	})
	select {
	case <-w.done:
	case <-time.After(time.Second):
		t.Fatal("failed open did not drain")
	}
	if err := w.stop(time.Second); err != nil || closed.Load() != 1 {
		t.Fatal("failed open resource not closed")
	}
	rr := httptest.NewRecorder()
	w.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != 503 {
		t.Fatal("failed open served")
	}
}

func TestMetadataWorkerCutoffDuringOpenJoinsClose(t *testing.T) {
	entered, release, closing, finish := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	w := beginMetadataWorker(context.Background(), true, func(context.Context) (metadataEndpoint, error) {
		close(entered)
		<-release
		return metadataEndpoint{handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("published after cutoff") }), close: func() error { close(closing); <-finish; return errors.New("private close error") }}, nil
	})
	<-entered
	if err := w.stop(time.Millisecond); err != errRestartMetadataShutdown {
		t.Fatal("acknowledged blocked open")
	}
	close(release)
	<-closing
	if err := w.stop(time.Millisecond); err != errRestartMetadataShutdown {
		t.Fatal("acknowledged blocked close")
	}
	close(finish)
	if err := w.stop(time.Second); err != errRestartMetadataShutdown || err.Error() != "restart metadata shutdown not acknowledged" {
		t.Fatal("close failure not redacted")
	}
}

func TestMetadataWorkerDrainsRequestBeforeClose(t *testing.T) {
	entered, release, canceled, closed := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := beginMetadataWorker(parent, true, func(context.Context) (metadataEndpoint, error) {
		return metadataEndpoint{handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			close(entered)
			<-r.Context().Done()
			close(canceled)
			<-release
		}), close: func() error { close(closed); return nil }}, nil
	})
	waitMetadataReady(t, w)
	served := make(chan struct{})
	go func() { defer close(served); w.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)) }()
	<-entered
	cancel()
	<-canceled
	if err := w.stop(time.Millisecond); err != errRestartMetadataShutdown {
		t.Fatal("acknowledged active request")
	}
	select {
	case <-closed:
		t.Fatal("closed before handler returned")
	default:
	}
	rr := httptest.NewRecorder()
	w.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != 503 {
		t.Fatal("new request admitted after cutoff")
	}
	close(release)
	<-served
	if err := w.stop(time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	default:
		t.Fatal("archive not closed")
	}
}
