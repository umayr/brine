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

func TestProcessSupervisionForkedChild(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for fork regression")
	}
	for _, mode := range []string{"cancel", "early-exit"} {
		t.Run(mode, func(t *testing.T) {
			activity := filepath.Join(t.TempDir(), "activity")
			// Each process has an independent hard lifetime, even if supervision is
			// broken. No test cleanup sends signals to a potentially reused PID.
			script := `import os,sys,time,signal
signal.alarm(3)
p=os.fork()
if p==0:
 signal.alarm(3)
 while True:
  with open(sys.argv[1],'ab',buffering=0) as f: f.write(b'x')
  time.sleep(0.02)
else:
 while not os.path.exists(sys.argv[1]): time.sleep(0.005)
 if sys.argv[2]=='early-exit': os._exit(0)
 while True: time.sleep(1)
`
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			cmd, cleanup, err := New(python).command(ctx, "-c", script, activity, mode)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			done := make(chan error, 1)
			go func() { done <- runSupervised(ctx, cmd) }()
			select {
			case err := <-done:
				if mode == "cancel" && err == nil {
					t.Fatal("cancellation succeeded")
				}
				if mode == "early-exit" && err != nil {
					t.Fatal(err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("supervisor did not terminate")
			}
			time.Sleep(100 * time.Millisecond)
			before, err := os.Stat(activity)
			if err != nil {
				t.Fatal("child did not start:", err)
			}
			time.Sleep(200 * time.Millisecond)
			after, err := os.Stat(activity)
			if err != nil {
				t.Fatal(err)
			}
			if before.Size() != after.Size() {
				t.Fatalf("child survived: %d -> %d", before.Size(), after.Size())
			}
		})
	}
}
