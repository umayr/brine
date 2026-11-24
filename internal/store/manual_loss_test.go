package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManualLedgerAndSidecarLossFailsClosed(t *testing.T) {
	root := t.TempDir()
	s, _ := NewFSStore(root)
	if _, err := s.ListManualIntents(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{manualIntentFile, "manual-deployments.lock"} {
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	reopened, _ := NewFSStore(root)
	if _, err := reopened.ListManualIntents(); err == nil {
		t.Fatal("lost established state reinitialized")
	}
}
func TestManualSidecarReplacementCannotSplitRootLock(t *testing.T) {
	root := t.TempDir()
	s, _ := NewFSStore(root)
	other, _ := NewFSStore(root)
	if _, err := s.ListManualIntents(); err != nil {
		t.Fatal(err)
	}
	err := s.manualTransaction(func(_ *[]ManualDeploymentIntent) error {
		name := filepath.Join(root, "manual-deployments.lock")
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := other.ListManualIntents(); err == nil {
			t.Fatal("replacement split serialization")
		}
		return nil
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.ListManualIntents(); err != nil {
		t.Fatal("root lock not released:", err)
	}
}
