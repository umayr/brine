//go:build linux || darwin

package metricshistory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/umayr/brine/internal/agentmetrics"
	"github.com/umayr/brine/internal/metricshistory/sqlitestore"
)

func TestRestartOnlyHistoryLifecycle(t *testing.T) {
	ctx, stopTest := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopTest()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	t.Setenv("TMPDIR", home)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// The proof's owner/ancestor rules reject synthetic roots in a sticky /tmp.
	if p, err := sqlitestore.OpenPersistent(dir, false); !os.IsNotExist(err) {
		if p != nil {
			_ = p.Close()
		}
		t.Skipf("no clean trusted synthetic root: %v", err)
	}
	id := sqlitestore.EnrollmentID{1}
	recorded := time.Unix(1700000000, 0).UTC()
	o := sqlitestore.Observation{Received: recorded, Sample: agentmetrics.Sample{HasHost: true, HostTotal: 1024}}
	if _, err := Start(StartupConfig{Directory: dir}); err == nil {
		t.Fatal("path alone enabled history")
	}
	if _, err := Start(StartupConfig{Enabled: true, Directory: dir}); err == nil {
		t.Fatal("startup initialized a store")
	}
	path := filepath.Join(dir, sqlitestore.HistoryFilename)
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("unprovisioned directory changed: %v", err)
	}
	if err := sqlitestore.InitializeObservations(dir); err != nil {
		t.Fatal(err)
	}
	h, err := Start(StartupConfig{Enabled: true, Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Stop(ctx) }()
	if err := h.Append(ctx, id, o); err != nil {
		t.Fatal(err)
	}
	admitted, ok := h.admit()
	if !ok {
		t.Fatal("failed to take synthetic local drain barrier")
	}
	released := false
	defer func() {
		if !released {
			h.release()
		}
	}()
	if err := h.Append(ctx, id, o); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("queued a second write: %v", err)
	}
	deadline, cancel := context.WithCancel(ctx)
	cancel()
	if err := h.Stop(deadline); !errors.Is(err, context.Canceled) {
		t.Fatalf("stop acknowledged before admitted work: %v", err)
	}
	if err := h.Append(ctx, id, o); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("write admitted after stop: %v", err)
	}
	if got, err := sqlitestore.OpenObservations(dir); !errors.Is(err, unix.EWOULDBLOCK) {
		if got != nil {
			_ = got.Close()
		}
		t.Fatalf("did not continue holding store lock before drain: %v", err)
	}
	o2 := o
	o2.Received = recorded.Add(time.Nanosecond)
	if err := admitted.Append(ctx, id, o2); err != nil {
		t.Fatalf("admitted write did not drain: %v", err)
	}
	h.release()
	released = true
	if err := h.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	off, err := Start(StartupConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := off.Append(ctx, id, o); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("disabled writer admitted sample: %v", err)
	}
	if err := off.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("disabled restart changed previous history")
	}
	restarted, err := Start(StartupConfig{Enabled: true, Directory: dir})
	if err != nil {
		t.Fatalf("explicit restart after drain failed: %v", err)
	}
	defer restarted.Stop(ctx)
	rows, err := restarted.Read(ctx, id, recorded, o2.Received, 2)
	if err != nil || len(rows) != 2 || rows[0] != o2 || rows[1] != o {
		t.Fatalf("restart did not preserve acknowledged captures: %v", err)
	}
}
