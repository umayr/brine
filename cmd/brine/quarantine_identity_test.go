package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/umayr/brine/internal/secretbindings"
	"github.com/umayr/brine/internal/store"
)

func TestEditorQuarantineUsesTrustedStackIdentity(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	st, err := store.NewFSStore(root)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := st.Create("demo", "x")
	if err != nil {
		t.Fatal(err)
	}
	q, err := secretbindings.NewQuarantine(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.Begin(secretbindings.DeploymentID("demo")); err != nil {
		t.Fatal(err)
	}
	reopened, err := secretbindings.OpenQuarantine(root)
	if err != nil {
		t.Fatal(err)
	}
	r, marker := fakeGateRunner(t, st)
	r.ConfigureQuarantine(reopened)
	for _, path := range []string{stack.PklPath, filepath.Join(t.TempDir(), "unregistered.pkl")} {
		if err := r.ValidateContent(context.Background(), path, "x"); err == nil {
			t.Fatal("validation bypassed identity/quarantine")
		}
		if out, err := r.Fmt(context.Background(), path, "x"); err == nil || out != "" {
			t.Fatalf("format bypassed denial: %q %v", out, err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("denied editor invoked process: %v", err)
	}
	if err := reopened.Check(secretbindings.DeploymentID("demo")); err == nil {
		t.Fatal("editor cleared intent")
	}
}
