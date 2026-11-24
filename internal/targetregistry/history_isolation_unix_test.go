//go:build linux || darwin

package targetregistry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agentmetrics"
	"github.com/umayr/brine/internal/metricshistory/sqlitestore"
)

func TestChangedHTTPSEnrollmentCannotReadPreviousHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	// The actual observation store rejects paths under a sticky /tmp.
	t.Setenv("TMPDIR", home)
	dir := privateRegistryDir(t)
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	r, err := OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	historyDir := filepath.Join(t.TempDir(), "history")
	if err := os.Mkdir(historyDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := sqlitestore.InitializeObservations(historyDir); err != nil {
		t.Fatal(err)
	}
	store, err := sqlitestore.OpenObservations(historyDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	old := syntheticHistoryTarget("example")
	replacement := old
	replacement.HTTPSPairing = PairingSecret(strings.Replace(string(old.HTTPSPairing), "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", 1))
	if replacement == old || replacement.Validate() != nil {
		t.Fatal("replacement must be a different, valid HTTPS enrollment")
	}
	if err := r.AddContext(ctx, old, old); err != nil {
		t.Fatal(err)
	}
	acquire := func(want Record) (HistoryIncarnation, *ReadLease) {
		t.Helper()
		got, id, lease, err := r.AcquireHistoryRead(ctx, want.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			_ = lease.Close()
			t.Fatal("history lease returned a different enrollment")
		}
		return id, lease
	}
	when := time.Unix(1700000000, 0).UTC()
	observation := sqlitestore.Observation{Received: when, Sample: agentmetrics.Sample{HasHost: true, HostTotal: 1024}}
	id1, lease := acquire(old)
	appendErr := store.Append(ctx, sqlitestore.EnrollmentID(id1), observation)
	if err := errors.Join(appendErr, lease.Close()); err != nil {
		t.Fatal(err)
	}

	// At revision 1 the candidate is valid but not a successor. Using the
	// required successor revision makes the candidate an invalid HTTPS record.
	if err := r.UpdateContext(ctx, old, replacement, ReviewedUpdate{old, replacement}); !errors.Is(err, ErrConflict) {
		t.Fatalf("in-place HTTPS update at revision 1: %v", err)
	}
	next := replacement
	next.Revision++
	if err := r.UpdateContext(ctx, old, next, ReviewedUpdate{old, next}); err == nil {
		t.Fatal("in-place HTTPS update at revision 2 admitted")
	}
	after, lease := acquire(old)
	if err := lease.Close(); err != nil || after != id1 {
		t.Fatal("rejected update changed current history association")
	}
	if err := r.ForgetHTTPS(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, lease, err := r.AcquireHistoryRead(ctx, old.ID); !errors.Is(err, ErrNotFound) || lease != nil {
		if lease != nil {
			_ = lease.Close()
		}
		t.Fatal("forgotten enrollment is still accessible")
	}
	if err := r.AddContext(ctx, replacement, replacement); err != nil {
		t.Fatal(err)
	}
	id2, lease := acquire(replacement)
	rows, readErr := store.Read(ctx, sqlitestore.EnrollmentID(id2), when, when, 1)
	if err := errors.Join(readErr, lease.Close()); err != nil || id2 == id1 || len(rows) != 0 {
		t.Fatalf("replacement selected earlier history: rows=%d err=%v", len(rows), err)
	}
	rows, err = store.Read(ctx, sqlitestore.EnrollmentID(id1), when, when, 1)
	if err != nil || len(rows) != 1 || rows[0] != observation {
		t.Fatal("previous observation was not preserved")
	}
}
