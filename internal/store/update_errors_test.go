package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUpdateDistinguishesMissingAndCorruptMetadata(t *testing.T) {
	s, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update("missing", "config {}"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing stack: %v", err)
	}
	if _, err := s.Create("demo", "config {}"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.stackDir("demo"), "meta.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update("demo", "config {}"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt metadata: %v", err)
	}
}
