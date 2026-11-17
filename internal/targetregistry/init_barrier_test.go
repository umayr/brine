package targetregistry

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestInitHoldsLockThroughFinalSync(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "sync-failure"}[fail], func(t *testing.T) {
			dir := privateRegistryDir(t)
			atFinalSync, proceed, waited := make(chan struct{}), make(chan struct{}), make(chan struct{})
			initDone := make(chan error, 1)
			go func() {
				initDone <- initRegistry(dir, initHooks{syncDir: func(stage string, f *os.File) error {
					if stage == "complete" {
						close(atFinalSync)
						<-proceed
						if fail {
							return errors.New("final sync failure")
						}
					}
					return f.Sync()
				}})
			}()
			<-atFinalSync
			if _, err := os.Stat(filepath.Join(dir, ".initialized-pending")); !os.IsNotExist(err) {
				t.Fatalf("pending marker remains: %v", err)
			}
			opened := make(chan error, 1)
			var once sync.Once
			go func() {
				r, err := openExisting(dir, func() { once.Do(func() { close(waited) }) })
				if err == nil {
					v := record()
					err = r.Add(v, v)
				}
				opened <- err
			}()
			select {
			case <-waited:
			case <-time.After(2 * time.Second):
				t.Fatal("opener never observed lock contention")
			}
			select {
			case err := <-opened:
				t.Fatalf("opener escaped lock: %v", err)
			default:
			}
			close(proceed)
			err := <-initDone
			if fail != errors.Is(err, ErrInitUncertain) {
				t.Fatalf("init result: %v", err)
			}
			select {
			case err := <-opened:
				if fail && err == nil {
					t.Fatal("opener accepted failed initialization")
				}
				if !fail && err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("opener did not finish")
			}
		})
	}
}

func TestInitSyncFailureRollbackAndUncertainty(t *testing.T) {
	for _, stage := range []string{"lock", "data", "pending", "marker", "complete"} {
		t.Run(stage, func(t *testing.T) {
			dir := privateRegistryDir(t)
			err := initRegistry(dir, initHooks{syncDir: func(s string, f *os.File) error {
				if s == stage {
					return errors.New("injected sync failure")
				}
				return f.Sync()
			}})
			if !errors.Is(err, ErrInitUncertain) {
				t.Fatalf("missing uncertain outcome: %v", err)
			}
			if _, err := OpenExisting(dir); err == nil {
				t.Fatal("opened after failed init/rollback")
			}
			if err := Init(dir); err == nil {
				t.Fatal("repaired partial state")
			}
		})
	}
}

func TestInitFileSyncAndCloseFailuresFailClosed(t *testing.T) {
	for _, name := range []string{"lock", "targets.json", ".initialized-pending"} {
		for _, failure := range []string{"sync", "close"} {
			t.Run(name+"/"+failure, func(t *testing.T) {
				dir := privateRegistryDir(t)
				hooks := initHooks{}
				if failure == "sync" {
					hooks.syncFile = func(n string, f *os.File) error {
						if n == name {
							return errors.New("sync failed")
						}
						return f.Sync()
					}
				}
				if failure == "close" {
					hooks.closeFile = func(n string, f *os.File) error {
						err := f.Close()
						if n == name {
							return errors.Join(err, errors.New("close failed"))
						}
						return err
					}
				}
				err := initRegistry(dir, hooks)
				if !errors.Is(err, ErrInitUncertain) {
					t.Fatalf("missing uncertain error: %v", err)
				}
				if name == "lock" && failure == "close" {
					return
				} // final lock close is explicitly quarantined, not automatically detectable
				if _, err := OpenExisting(dir); err == nil {
					t.Fatal("opened incomplete state")
				}
			})
		}
	}
}

func TestInitRollbackAndFinalCloseFailuresRequireManualQuarantine(t *testing.T) {
	for _, scenario := range []string{"rollback", "lock-close"} {
		t.Run(scenario, func(t *testing.T) {
			dir := privateRegistryDir(t)
			hooks := initHooks{}
			if scenario == "rollback" {
				hooks.syncDir = func(stage string, f *os.File) error {
					if stage == "complete" {
						return errors.New("sync failed")
					}
					return f.Sync()
				}
				hooks.removeMarker = func(string) error { return errors.New("remove failed") }
			}
			if scenario == "lock-close" {
				hooks.closeFile = func(name string, f *os.File) error {
					err := f.Close()
					if name == "lock" {
						return errors.Join(err, errors.New("close failed"))
					}
					return err
				}
			}
			err := initRegistry(dir, hooks)
			if !errors.Is(err, ErrInitUncertain) {
				t.Fatalf("missing uncertain outcome: %v", err)
			}
			// This demonstrates why the operator MUST quarantine all consumers: the
			// registry can appear complete after these errors and cannot self-attest.
			if _, err := OpenExisting(dir); err != nil {
				t.Fatalf("expected ambiguous state to illustrate manual quarantine: %v", err)
			}
		})
	}
}

func TestLockReplacementWithActiveLeaseIsOutsideTrustBoundary(t *testing.T) {
	dir := privateRegistryDir(t)
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	old, err := OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	v := record()
	if err := old.Add(v, v); err != nil {
		t.Fatal(err)
	}
	_, lease, err := old.AcquireRead(t.Context(), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	path := filepath.Join(dir, ".lock")
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	fresh, err := OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	next := v
	next.ID = "other"
	done := make(chan error, 1)
	go func() { done <- fresh.Add(next, next) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh lock unexpectedly coordinated with old lease")
	}
	if _, err := old.List(); err == nil {
		t.Fatal("old registry failed to notice replacement")
	}
}
