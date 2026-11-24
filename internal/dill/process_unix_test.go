//go:build linux || darwin

package dill

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessCancellationKillsForkedChild(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for fork regression")
	}
	activity := filepath.Join(t.TempDir(), "activity")
	// The child runs Python code after fork, without exec. The parent remains
	// alive so cancellation must terminate both members of the owned group.
	script := `import os, sys, time
p = os.fork()
if p == 0:
    while True:
        with open(sys.argv[1], 'ab', buffering=0) as f:
            f.write(b'x')
        time.sleep(0.02)
else:
    while True:
        time.sleep(1)
`
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, cleanup, err := New(python).command(ctx, "-c", script, activity)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if info, err := os.Stat(activity); err == nil && info.Size() > 0 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("forked child did not start writing")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled command succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled command did not return")
	}
	// Allow an in-flight write to finish before checking for continued activity.
	time.Sleep(150 * time.Millisecond)
	before, err := os.Stat(activity)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	after, err := os.Stat(activity)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() {
		t.Fatalf("forked child continued writing: %d -> %d", before.Size(), after.Size())
	}
}
