//go:build !linux && !darwin

package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedPublicationFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewFSRecipeStore(dir); err == nil {
		t.Fatal("publication unexpectedly succeeded")
	}
	if _, err := os.Lstat(filepath.Join(dir, "recipes")); !os.IsNotExist(err) {
		t.Fatalf("library published: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "recipes"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFSRecipeStore(dir); err != nil {
		t.Fatal(err)
	}
}
