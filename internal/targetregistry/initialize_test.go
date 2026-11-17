package targetregistry

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func privateRegistryDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "registry")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestStrictRegistryLifecycleAndRestart(t *testing.T) {
	dir := privateRegistryDir(t)
	if _, err := OpenExisting(dir); err == nil {
		t.Fatal("opened uninitialized registry")
	}
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir); err == nil {
		t.Fatal("reinitialized")
	}
	r, err := OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	v := record()
	if err := r.Add(v, v); err != nil {
		t.Fatal(err)
	}
	r, err = OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(v.ID)
	if err != nil || got != v {
		t.Fatalf("restart: %v %v", got, err)
	}
}

func TestStrictRegistryMissingOrCorruptFailsClosed(t *testing.T) {
	for _, name := range []string{".lock", ".initialized", "targets.json"} {
		for _, corrupt := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/missing", true: "/corrupt"}[corrupt], func(t *testing.T) {
				dir := privateRegistryDir(t)
				if err := Init(dir); err != nil {
					t.Fatal(err)
				}
				r, err := OpenExisting(dir)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, name)
				if corrupt {
					if name == ".lock" {
						if err := os.Chmod(path, 0644); err != nil {
							t.Fatal(err)
						}
					} else if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if _, err := r.List(); err == nil {
					t.Fatal("list accepted invalid state")
				}
				v := record()
				if err := r.Add(v, v); err == nil {
					t.Fatal("add accepted invalid state")
				}
				if _, err := OpenExisting(dir); err == nil {
					t.Fatal("restart accepted invalid state")
				}
				if !corrupt {
					if _, err := os.Lstat(path); !os.IsNotExist(err) {
						t.Fatalf("missing state recreated: %v", err)
					}
				}
			})
		}
	}
}

func TestStrictNonemptyLockFails(t *testing.T) {
	dir := privateRegistryDir(t)
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	r, err := OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".lock"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.List(); err == nil {
		t.Fatal("accepted nonempty lock")
	}
	if _, err := OpenExisting(dir); err == nil {
		t.Fatal("restart accepted nonempty lock")
	}
}

func TestStrictLockReplacementFails(t *testing.T) {
	dir := privateRegistryDir(t)
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	r, err := OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".lock")
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.List(); err == nil {
		t.Fatal("accepted replacement lock")
	}
}

func TestStrictDeletionBeforePublicationFails(t *testing.T) {
	for _, name := range []string{".lock", ".initialized", "targets.json"} {
		t.Run(name, func(t *testing.T) {
			dir := privateRegistryDir(t)
			if err := Init(dir); err != nil {
				t.Fatal(err)
			}
			r, err := OpenExisting(dir)
			if err != nil {
				t.Fatal(err)
			}
			r.beforePublish = func() {
				if err := os.Remove(filepath.Join(dir, name)); err != nil {
					t.Error(err)
				}
			}
			v := record()
			if err := r.Add(v, v); err == nil {
				t.Fatal("published after deletion")
			}
			if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
				t.Fatalf("state recreated: %v", err)
			}
		})
	}
}

func TestInitRejectsPartialAndConcurrent(t *testing.T) {
	for _, name := range []string{".lock", ".initialized", "targets.json"} {
		t.Run(name, func(t *testing.T) {
			dir := privateRegistryDir(t)
			if err := os.WriteFile(filepath.Join(dir, name), []byte("partial"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := Init(dir); err == nil {
				t.Fatal("accepted partial state")
			}
			if _, err := OpenExisting(dir); err == nil {
				t.Fatal("opened partial state")
			}
		})
	}
	dir := privateRegistryDir(t)
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- Init(dir) }()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("successful initializers = %d", success)
	}
	if _, err := OpenExisting(dir); err != nil {
		t.Fatal(err)
	}
}
