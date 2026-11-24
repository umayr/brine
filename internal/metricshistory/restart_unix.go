//go:build linux || darwin

// Package metricshistory implements restart-scoped Brine history ownership.
// It exposes no routes and does not collect or independently authorize metrics.
package metricshistory

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/umayr/brine/internal/metricshistory/sqlitestore"
)

var ErrUnavailable = errors.New("metrics history is unavailable")

// StartupConfig is supplied by the operator's persistent launch configuration.
// Setting a directory alone does not grant startup permission.
type StartupConfig struct {
	Enabled   bool
	Directory string
	MaxPages  int
}

// History owns the open store for one server lifetime. Stop is one-way. The
// caller owns target/ACL/freshness checks, provisioning, and persistent config.
// A History must not be copied after first use.
type History struct {
	mu       sync.Mutex
	store    *sqlitestore.ObservationStore
	stop     chan struct{}
	stopErr  error // published by closing stop
	busy     bool
	inFlight sync.WaitGroup
}

// Start opens an already provisioned store only upon explicit startup opt-in.
// This function does not mount a handler, start a collector, adopt a prototype,
// or modify persistent config. The enabled path is local-filesystem only.
func Start(config StartupConfig) (*History, error) {
	if !config.Enabled {
		if config.Directory != "" || config.MaxPages != 0 {
			return nil, errors.New("history directory requires explicit enablement")
		}
		return &History{}, nil
	}
	if config.Directory == "" {
		return nil, errors.New("enabled history requires a provisioned directory")
	}
	store, err := sqlitestore.OpenBoundedObservations(config.Directory, config.MaxPages)
	if err != nil {
		return nil, err
	}
	return &History{store: store}, nil
}

func (h *History) admit() (*sqlitestore.ObservationStore, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stop != nil || h.store == nil || h.busy {
		return nil, false
	}
	h.busy = true
	h.inFlight.Add(1)
	return h.store, true
}

func (h *History) release() {
	h.mu.Lock()
	h.busy = false
	h.inFlight.Done()
	h.mu.Unlock()
}

// Append requires an already authorized enrollment and verified observation.
// The live read path should never depend on the success of this optional write.
func (h *History) Append(ctx context.Context, id sqlitestore.EnrollmentID, o sqlitestore.Observation) error {
	store, ok := h.admit()
	if !ok {
		return ErrUnavailable
	}
	defer h.release()
	return store.Append(ctx, id, o)
}

// Read requires a caller-authorized enrollment and returns bounded stored rows.
func (h *History) Read(ctx context.Context, id sqlitestore.EnrollmentID, from, through time.Time, limit int) ([]sqlitestore.Observation, error) {
	store, ok := h.admit()
	if !ok {
		return nil, ErrUnavailable
	}
	defer h.release()
	return store.Read(ctx, id, from, through, limit)
}

// Cutoff returns once future reads and appends are refused; it does not wait
// for already admitted work or closing the database.
func (h *History) Cutoff() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stop == nil {
		h.stop = make(chan struct{})
		if h.store == nil {
			close(h.stop)
		} else {
			go h.drain(h.store, h.stop)
		}
	}
}

// Stop acknowledges local drain and store Close, not external stop or config
// persistence. Deadline/cancellation ends only the wait.
func (h *History) Stop(ctx context.Context) error {
	h.Cutoff()
	h.mu.Lock()
	stopped := h.stop
	h.mu.Unlock()
	select {
	case <-stopped:
		return h.stopErr
	default:
	}
	select {
	case <-stopped:
		return h.stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *History) drain(store *sqlitestore.ObservationStore, stopped chan struct{}) {
	h.inFlight.Wait()
	h.stopErr = store.Close()
	close(stopped)
}
