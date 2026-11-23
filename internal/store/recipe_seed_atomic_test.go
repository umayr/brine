package store

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRecipeSeedFailureRetries(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("atomic publication unsupported")
	}
	dir := t.TempDir()
	_, err := newFSRecipeStore(dir, func(s *FSRecipeStore) error {
		if err := os.Mkdir(filepath.Join(s.dataDir, "recipes", "partial"), 0700); err != nil {
			return err
		}
		return errors.New("injected failure")
	}, publishRecipes)
	if err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Lstat(filepath.Join(dir, "recipes")); !os.IsNotExist(err) {
		t.Fatalf("library published: %v", err)
	}
	assertAttempts(t, dir, 0)
	s, err := NewFSRecipeStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	recipes, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"Simple Web Service": false, "PostgreSQL Database": false, "Redis Cache": false, "Health Check": false, "Resource Limits": false, "Traefik Labels": false}
	if len(recipes) != len(want) {
		t.Fatalf("unexpected recipe count: %d", len(recipes))
	}
	for _, r := range recipes {
		if _, ok := want[r.Name]; !ok {
			t.Fatalf("unexpected recipe %q", r.Name)
		}
		want[r.Name] = true
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing %q", name)
		}
	}
	assertAttempts(t, dir, 0)
}

func assertAttempts(t *testing.T, dir string, count int) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, ".recipes-attempt-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != count {
		t.Fatalf("staging attempts: %v", paths)
	}
	return paths
}

func TestSeedReturnsCreateError(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "recipes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "recipes", "simple-web-service"), []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := (&FSRecipeStore{dataDir: dir}).seed(); err == nil {
		t.Fatal("seed ignored create error")
	}
}

func TestExistingRecipeLibrariesUntouched(t *testing.T) {
	for _, name := range []string{"empty", "partial", "custom"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			lib := filepath.Join(dir, "recipes")
			if err := os.Mkdir(lib, 0700); err != nil {
				t.Fatal(err)
			}
			if name != "empty" {
				if err := os.Mkdir(filepath.Join(lib, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chmod(lib, 0755); err != nil {
				t.Fatal(err)
			}
			if _, err := NewFSRecipeStore(dir); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(lib)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0755 {
				t.Fatalf("existing mode changed: %o", info.Mode().Perm())
			}
			entries, err := os.ReadDir(lib)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if name == "empty" {
				want = 0
			}
			if len(entries) != want {
				t.Fatalf("changed library: %v", entries)
			}
		})
	}
}
