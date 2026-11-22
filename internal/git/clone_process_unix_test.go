//go:build linux || darwin

package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCloneCancelsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "git")
	ready := filepath.Join(dir, "ready")
	late := filepath.Join(dir, "late")
	script := "#!/bin/sh\n(sleep 0.5; echo late > '" + late + "') &\necho ready > '" + ready + "'\nwait\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- (&Runner{Binary: bin}).Clone(ctx, "synthetic", "main", filepath.Join(dir, "repo")) }()
	deadline := time.After(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("clone exited before handshake: %v", err)
		case <-deadline:
			t.Fatal("helper did not start")
		case <-time.After(5 * time.Millisecond):
		}
	}
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation")
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("clone did not terminate promptly")
	}
	if time.Since(start) > 300*time.Millisecond {
		t.Fatal("slow cancellation")
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(late); !os.IsNotExist(err) {
		t.Fatalf("helper wrote after cancellation: %v", err)
	}
}
