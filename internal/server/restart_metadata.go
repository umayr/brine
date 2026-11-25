package server

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

var errRestartMetadataShutdown = errors.New("restart metadata shutdown not acknowledged")

// metadataEndpoint is owned exclusively by the worker. close must release any
// archive handle even when open returns an error after acquiring resources.
type metadataEndpoint struct {
	handler http.Handler
	close   func() error
}

type metadataWorker struct {
	life     context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	mu       sync.Mutex
	handler  http.Handler
	stopped  bool
	requests sync.WaitGroup
	err      error // published by closing done
}

// beginMetadataWorker does no work when disabled. The opener must only open
// existing, validated state; neither this worker nor a request may initialize
// or repair an archive. Cancellation is not a guarantee of bounded disk I/O.
func beginMetadataWorker(parent context.Context, enabled bool, open func(context.Context) (metadataEndpoint, error)) *metadataWorker {
	if !enabled {
		return nil
	}
	life, cancel := context.WithCancel(parent)
	w := &metadataWorker{life: life, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		var endpoint metadataEndpoint
		var err error
		if life.Err() == nil && open != nil {
			endpoint, err = open(life)
		}
		w.mu.Lock()
		if err == nil && endpoint.handler != nil && endpoint.close != nil && !w.stopped && life.Err() == nil {
			w.handler = endpoint.handler
		}
		ready := w.handler != nil
		w.mu.Unlock()
		if ready {
			<-life.Done()
		}
		w.cutoff()
		w.requests.Wait()
		if endpoint.close != nil && endpoint.close() != nil {
			w.err = errRestartMetadataShutdown
		}
	}()
	return w
}

func (w *metadataWorker) cutoff() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.stopped = true
	w.handler = nil
	w.cancel()
	w.mu.Unlock()
}

func (w *metadataWorker) stop(grace time.Duration) error {
	if w == nil {
		return nil
	}
	w.cutoff()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-w.done:
		return w.err
	case <-timer.C:
		return errRestartMetadataShutdown
	}
}

func (w *metadataWorker) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Cache-Control", "no-store")
	if w == nil {
		http.Error(rw, "Restart metadata unavailable", http.StatusServiceUnavailable)
		return
	}
	w.mu.Lock()
	h := w.handler
	if w.stopped || w.life.Err() != nil || h == nil {
		w.mu.Unlock()
		http.Error(rw, "Restart metadata unavailable", http.StatusServiceUnavailable)
		return
	}
	w.requests.Add(1) // serialized with cutoff, before the worker may Wait
	w.mu.Unlock()
	defer w.requests.Done()
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(w.life, cancel)
	defer func() { stop(); cancel() }()
	if w.life.Err() != nil {
		cancel()
	}
	h.ServeHTTP(rw, r.WithContext(ctx))
}
