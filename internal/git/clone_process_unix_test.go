//go:build linux || darwin

package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const noisyCloneOutput = `printf 'synthetic clone stdout\n'
printf 'synthetic clone stderr\n' >&2
head -c 1048576 /dev/zero
head -c 1048576 /dev/zero >&2
`

func TestCloneDiscardsNoisyFailureOutput(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-git")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+noisyCloneOutput+"exit 42\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := (&Runner{Binary: bin}).Clone(ctx, "synthetic", "main", filepath.Join(dir, "repo"))
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 42 {
		t.Fatalf("clone did not preserve exit code 42: %T", err)
	}
	if len(err.Error()) > 256 || strings.Contains(err.Error(), "synthetic clone") {
		t.Fatal("clone returned subprocess output")
	}
}

func TestCloneCancelsNoisyProcessGroup(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "git")
	ready := filepath.Join(dir, "ready")
	late := filepath.Join(dir, "late")
	script := "#!/bin/sh\n" + noisyCloneOutput + "(sleep 0.5; echo late > '" + late + "') &\necho ready > '" + ready + "'\nwait\n"
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
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation: %T", err)
		}
		if len(err.Error()) > 256 || strings.Contains(err.Error(), "synthetic clone") {
			t.Fatal("clone returned subprocess output after cancellation")
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
