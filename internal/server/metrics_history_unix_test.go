//go:build linux || darwin

package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/metricshistory"
	"github.com/umayr/brine/internal/metricshistory/sqlitestore"
)

func TestOptionalHistoryWorkerDoesNotWaitForStartup(t *testing.T) {
	ctx := context.Background()
	if w := beginOptionalMetricsHistory(ctx, &config.Config{}); w != nil {
		t.Fatal("default-off scheduled history work")
	}
	started, release := make(chan struct{}), make(chan struct{})
	w := beginMetricsHistory(ctx, func(ctx context.Context) optionalMetricsHistory {
		close(started)
		<-release // synthetic blocked startup I/O
		return optionalMetricsHistory{state: historyOff}
	})
	defer func() {
		close(release)
		if err := w.stop(time.Second); err != nil {
			t.Errorf("failed to join drained synthetic worker: %v", err)
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("history worker did not start")
	}
	if err := w.stop(5 * time.Millisecond); !errors.Is(err, errHistoryShutdown) {
		t.Fatalf("acknowledged stop before startup returned: %v", err)
	}
}

type closingHistory struct {
	cutoff chan struct{}
	drain  chan struct{}
	once   sync.Once
}

func (h *closingHistory) Cutoff() { h.once.Do(func() { close(h.cutoff) }) }

func (h *closingHistory) Stop(ctx context.Context) error {
	h.Cutoff()
	select {
	case <-h.drain:
		return errors.New("synthetic unsafe raw close message")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestHistoryCutoffDuringOpenDrainsAndRedactsCloseFailure(t *testing.T) {
	h := &closingHistory{cutoff: make(chan struct{}), drain: make(chan struct{})}
	started, publish := make(chan struct{}), make(chan struct{})
	w := beginMetricsHistory(context.Background(), func(context.Context) optionalMetricsHistory {
		close(started)
		<-publish
		return optionalMetricsHistory{state: historyReady, store: h}
	})
	unblock := sync.OnceFunc(func() { close(publish) })
	finish := sync.OnceFunc(func() { close(h.drain) })
	defer unblock()
	defer finish()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("optional startup did not begin")
	}
	w.cutoff()
	unblock()
	select {
	case <-h.cutoff:
	case <-time.After(time.Second):
		t.Fatal("store opened after cutoff without closing admission")
	}
	w.mu.Lock()
	late := w.store
	w.mu.Unlock()
	if late != nil {
		t.Fatal("canceled startup published its store")
	}
	select {
	case <-w.done:
		t.Fatal("shutdown acknowledged before close completed")
	default:
	}
	finish()
	if err := w.stop(time.Second); err != errHistoryShutdown {
		t.Fatalf("close failure did not surface as sanitized shutdown error: %v", err)
	}
}

func TestOptionalHistoryStartupIsIndependent(t *testing.T) {
	ctx := context.Background()
	if base := os.Getenv("BRINE_TEST_TRUSTED_ROOT"); base != "" {
		t.Setenv("TMPDIR", base)
	}
	root := t.TempDir()
	makeDir := func(name string) string {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	history := makeDir("history")
	if h, err := sqlitestore.OpenPersistent(history, false); !os.IsNotExist(err) {
		if h != nil {
			_ = h.Close()
		}
		t.Skipf("unsafe synthetic root: %v", err)
	}
	cfg := &config.Config{RemoteEnabled: true, DataDir: makeDir("data"), RemoteRegistryDir: makeDir("registry")}
	open := func(cfg metricshistory.StartupConfig) (historyHandle, error) {
		h, err := metricshistory.Start(cfg)
		if err != nil {
			return nil, err
		}
		return h, nil
	}
	check := func(want historyState) {
		t.Helper()
		h := startOptionalMetricsHistory(ctx, cfg, open)
		if h.store != nil {
			_ = h.stop(time.Second)
		}
		if h.state != want || h.store != nil {
			t.Fatalf("history startup state %v, want %v and no store", h.state, want)
		}
	}
	check(historyOff)
	cfg.MetricsHistoryDir = history
	check(historyUnavailable) // no implicit enable from a path
	cfg.MetricsHistoryEnabled = true
	check(historyUnavailable) // explicit capacity also required
	cfg.MetricsHistoryMaxPages = 1024
	check(historyUnavailable) // no implicit provisioning
	path := filepath.Join(history, sqlitestore.HistoryFilename)
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("optional startup created a database")
	}
	if err := sqlitestore.InitializeObservations(history); err != nil {
		t.Fatal(err)
	}
	cfg.NoAuth = true
	check(historyUnavailable)
	cfg.NoAuth = false
	original := cfg.DataDir
	cfg.DataDir = history
	check(historyUnavailable)
	cfg.DataDir = filepath.Join(root, "alias")
	if err := os.Symlink(history, cfg.DataDir); err != nil {
		t.Fatal(err)
	}
	check(historyUnavailable)
	cfg.DataDir = original
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if h := startOptionalMetricsHistory(canceled, cfg, open); h.state != historyOff || h.store != nil {
		t.Fatal("canceled startup opened history")
	}
	if h := startOptionalMetricsHistory(ctx, cfg, nil); h.state != historyUnavailable || h.store != nil {
		t.Fatal("valid options without product qualification opened history")
	}
	h := startOptionalMetricsHistory(ctx, cfg, open)
	if h.state != historyReady || h.store == nil {
		t.Fatal("approved synthetic settings did not open the initialized store")
	}
	t.Cleanup(func() { _ = h.stop(time.Second) })
	check(historyUnavailable) // no takeover when another instance holds the lock
	if err := h.stop(time.Second); err != nil {
		t.Fatalf("server shutdown failed to acknowledge drain: %v", err)
	}
	if err := h.store.(*metricshistory.History).Append(ctx, sqlitestore.EnrollmentID{1}, sqlitestore.Observation{}); !errors.Is(err, metricshistory.ErrUnavailable) {
		t.Fatal("server shutdown cutoff left admission open")
	}
	two := startOptionalMetricsHistory(ctx, cfg, open)
	if two.state != historyReady || two.store == nil {
		t.Fatal("validated restart could not reacquire the store")
	}
	if err := two.stop(time.Second); err != nil {
		t.Fatal(err)
	}
}
