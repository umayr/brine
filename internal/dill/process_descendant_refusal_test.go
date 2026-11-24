//go:build linux || darwin

package dill

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestRefusedGroupRetainsLiveDescendant(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for fork regression")
	}
	activity := filepath.Join(t.TempDir(), "activity")
	// Independent alarms bound both processes even if the implementation breaks.
	script := `import os,sys,time,signal
signal.alarm(8)
p=os.fork()
if p==0:
 signal.alarm(8)
 while True:
  with open(sys.argv[1],'ab',buffering=0) as f: f.write(b'x')
  time.sleep(0.02)
else:
 while not os.path.exists(sys.argv[1]): time.sleep(0.005)
 os._exit(0)
`
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-c", script, activity)
	configureProcessCancellation(cmd)
	before := len(processSlots)
	var allow atomic.Bool
	// Cleanup goes through the retained owner, never signals a possibly reused PID.
	defer func() {
		allow.Store(true)
		deadline := time.Now().Add(4 * time.Second)
		for len(processSlots) != before && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if len(processSlots) != before {
			t.Error("retained owner failed to reclaim slot")
		}
	}()
	err = runOwnedProcess(ctx, cmd, func(pid int) error {
		if !allow.Load() {
			return syscall.EPERM
		}
		return groupCleanupError(pid, syscall.Kill(-pid, syscall.SIGKILL))
	}, processExited)
	if !errors.Is(err, ErrProcessUnknown) {
		t.Fatalf("expected unknown: %v", err)
	}
	if len(processSlots) != before+1 {
		t.Fatal("dead leader lost live descendant ownership")
	}
	first, err := os.Stat(activity)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	last, err := os.Stat(activity)
	if err != nil {
		t.Fatal(err)
	}
	if last.Size() <= first.Size() {
		t.Fatal("fixture did not retain live descendant")
	}
	if len(processSlots) != before+1 {
		t.Fatal("live descendant released slot")
	}
}
