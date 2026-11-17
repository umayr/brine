package targetregistry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type cancelOnFourthCheck struct {
	context.Context
	calls int
}

func (c *cancelOnFourthCheck) Err() error {
	c.calls++
	if c.calls >= 4 {
		return context.Canceled
	}
	return nil
}

func TestLockFailureCleanupIsReportedWithoutDetails(t *testing.T) {
	for _, scenario := range []string{"invalid-lock", "missing-data", "not-found", "cancelled", "list", "write"} {
		t.Run(scenario, func(t *testing.T) {
			dir := privateRegistryDir(t)
			if err := Init(dir); err != nil {
				t.Fatal(err)
			}
			r, err := OpenExisting(dir)
			if err != nil {
				t.Fatal(err)
			}
			r.closeFile = func(f *os.File) error { _ = f.Close(); return errors.New("sensitive cleanup detail") }
			ctx := context.Context(context.Background())
			switch scenario {
			case "invalid-lock":
				if err := os.Chmod(filepath.Join(dir, ".lock"), 0644); err != nil {
					t.Fatal(err)
				}
			case "missing-data":
				if err := os.Remove(filepath.Join(dir, "targets.json")); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				ctx = &cancelOnFourthCheck{Context: context.Background()}
			}
			switch scenario {
			case "list":
				_, err = r.List()
			case "write":
				v := record()
				err = r.Add(v, v)
			default:
				_, _, err = r.AcquireRead(ctx, "unknown")
			}
			if !errors.Is(err, ErrCleanup) {
				t.Fatalf("missing cleanup sentinel: %v", err)
			}
			if strings.Contains(err.Error(), "sensitive cleanup detail") {
				t.Fatalf("leaked cleanup detail: %v", err)
			}
			if scenario == "not-found" && !errors.Is(err, ErrNotFound) {
				t.Fatalf("missing not-found cause: %v", err)
			}
			if scenario == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("missing cancellation cause: %v", err)
			}
		})
	}
}
