//go:build linux || darwin

package sqlitestore

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agentmetrics"
)

func TestBoundedObservationsSuspendWithoutPruning(t *testing.T) {
	if base := os.Getenv("BRINE_TEST_TRUSTED_ROOT"); base != "" {
		t.Setenv("TMPDIR", base)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	probe, err := openHistoryDir(dir)
	if err != nil {
		if os.Getenv("BRINE_TEST_TRUSTED_ROOT") == "" {
			t.Skip("set BRINE_TEST_TRUSTED_ROOT to an isolated trusted fixture root")
		}
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	if err := InitializeObservations(dir); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenBoundedObservations(dir, 1); err == nil {
		_ = s.Close()
		t.Fatal("accepted an impossible cap")
	}
	s, err := OpenBoundedObservations(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// Force pool replacement after each operation: the cap cannot depend on
	// the connection used for validation or any prior append.
	s.root.store.db.SetMaxIdleConns(0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id := EnrollmentID{1}
	first := time.Unix(1700000000, 0).UTC()
	observation := Observation{Received: first, Sample: agentmetrics.Sample{HasHost: true, HostTotal: 4096}}
	written := 0
	for ; written < 1000; written++ {
		observation.Received = first.Add(time.Duration(written))
		if err := s.Append(ctx, id, observation); err != nil {
			if !errors.Is(err, ErrObservationCapacity) {
				t.Fatal(err)
			}
			break
		}
	}
	if written < 1 || written >= 1000 {
		t.Fatalf("expected a whole-capture refusal after committed samples, written=%d", written)
	}
	if err := s.Append(ctx, id, observation); !errors.Is(err, ErrObservationWritesSuspended) {
		t.Fatalf("writes not suspended: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenBoundedObservations(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.Read(ctx, id, first, observation.Received, written+1)
	if err != nil || len(rows) != written || !rows[0].Received.Equal(observation.Received.Add(-time.Nanosecond)) || !rows[written-1].Received.Equal(first) {
		t.Fatalf("reopen did not preserve exactly prior acknowledged rows: got=%d want=%d err=%v", len(rows), written, err)
	}
}
