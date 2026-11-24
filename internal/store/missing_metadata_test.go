package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMissingMetadataIsNotMissingRegistration(t *testing.T) {
	root := t.TempDir()
	s, err := NewFSStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("absent"); !errors.Is(err, ErrStackNotFound) {
		t.Fatalf("absent registration: %v", err)
	}
	if _, err := s.Create("present", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "stacks", "present", "meta.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("present"); err == nil || errors.Is(err, ErrStackNotFound) {
		t.Fatalf("missing metadata must fail closed: %v", err)
	}
}
