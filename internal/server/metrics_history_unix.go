//go:build linux || darwin

package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/metricshistory"
)

type historyState uint8

const (
	historyOff historyState = iota
	historyUnavailable
	historyReady
)

type optionalMetricsHistory struct {
	state historyState
	store *metricshistory.History
}

var errHistoryShutdown = errors.New("metrics history shutdown not acknowledged")

// metricsHistoryWorker owns asynchronous, optional startup. Startup I/O cannot
// delay live listen/serve; a hung initialization cannot produce a valid stop ACK.
// There is no mounted accessor to the worker or store.
type metricsHistoryWorker struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error // published by closing done
}

func beginOptionalMetricsHistory(ctx context.Context, cfg *config.Config) *metricsHistoryWorker {
	if !cfg.MetricsHistoryEnabled && cfg.MetricsHistoryDir == "" {
		return nil // default off does not schedule work or inspect the filesystem
	}
	return beginMetricsHistory(ctx, func(ctx context.Context) optionalMetricsHistory {
		return startOptionalMetricsHistory(ctx, cfg)
	})
}

func beginMetricsHistory(ctx context.Context, open func(context.Context) optionalMetricsHistory) *metricsHistoryWorker {
	life, cancel := context.WithCancel(ctx)
	w := &metricsHistoryWorker{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		h := open(life)
		if life.Err() == nil {
			h.reportStartup()
		}
		if h.store != nil {
			<-life.Done()
			w.err = h.stop(5 * time.Second)
		}
	}()
	return w
}

func (w *metricsHistoryWorker) cutoff() {
	if w != nil {
		w.cancel()
	}
}

func (w *metricsHistoryWorker) stop(grace time.Duration) error {
	if w == nil {
		return nil
	}
	w.cutoff()
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	select {
	case <-w.done:
		return w.err
	case <-ctx.Done():
		return errHistoryShutdown
	}
}

// startOptionalMetricsHistory never returns an error to the live server. Raw
// database/OS errors, paths and enrollment material are not reported here.
func startOptionalMetricsHistory(ctx context.Context, cfg *config.Config) optionalMetricsHistory {
	if ctx.Err() != nil {
		return optionalMetricsHistory{state: historyOff}
	}
	dir, err := cfg.MetricsHistoryStartupDir()
	if err != nil {
		return optionalMetricsHistory{state: historyUnavailable}
	}
	if dir == "" || ctx.Err() != nil {
		return optionalMetricsHistory{state: historyOff}
	}
	h, err := metricshistory.Start(metricshistory.StartupConfig{Enabled: true, Directory: dir})
	if err != nil {
		return optionalMetricsHistory{state: historyUnavailable}
	}
	return optionalMetricsHistory{state: historyReady, store: h}
}

func (h optionalMetricsHistory) reportStartup() {
	if h.state == historyUnavailable {
		fmt.Fprintln(os.Stderr, "optional metrics history unavailable")
	}
}

func (h optionalMetricsHistory) stop(grace time.Duration) error {
	if h.store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := h.store.Stop(ctx); err != nil {
		return errHistoryShutdown
	}
	return nil
}
