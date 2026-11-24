//go:build linux || darwin

package dill

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type shortProcessWriter struct{}

func (shortProcessWriter) Write(p []byte) (int, error) { return 0, nil }
func TestProcessShortWriteReported(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "/bin/echo", "hello")
	configureProcessCancellation(cmd)
	cmd.Stdout = shortProcessWriter{}
	if err := runSupervised(context.Background(), cmd); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write lost: %v", err)
	}
}

func TestProcessObservationFailureReturnsUnknownAndRetainsOwnership(t *testing.T) {
	before := len(processSlots)
	var visible atomic.Bool
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "printf ready; sleep 3")
	configureProcessCancellation(cmd)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	observe := func(pid int) (bool, error) {
		if !visible.Load() {
			return false, syscall.EPERM
		}
		return processExited(pid)
	}
	start := time.Now()
	err := runOwnedProcess(context.Background(), cmd, func(pid int) error { return groupCleanupError(pid, syscall.Kill(-pid, syscall.SIGKILL)) }, observe)
	if !errors.Is(err, ErrProcessUnknown) {
		t.Errorf("unknown lost: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("unknown return exceeded bound")
	}
	if len(processSlots) != before+1 {
		t.Error("unobserved leader ownership released")
	}
	saved := out.String()
	visible.Store(true)
	deadline := time.Now().Add(4 * time.Second)
	for len(processSlots) != before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(processSlots) != before {
		t.Fatal("retained supervisor did not reap")
	}
	if out.String() != saved {
		t.Fatal("output changed after return")
	}
}

func TestProcessFailedGroupSignalIsUnknown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sleep", "3")
	configureProcessCancellation(cmd)
	before := len(processSlots)
	var allowCleanup atomic.Bool
	err := runOwnedProcess(ctx, cmd, func(pid int) error {
		if !allowCleanup.Load() {
			return syscall.EPERM
		}
		return groupCleanupError(pid, syscall.Kill(-pid, syscall.SIGKILL))
	}, processExited)
	if !errors.Is(err, ErrProcessUnknown) || !errors.Is(err, syscall.EPERM) {
		t.Errorf("cleanup failure hidden: %v", err)
	}
	if len(processSlots) != before+1 {
		t.Error("dead leader released refused group ownership")
	}
	allowCleanup.Store(true)
	deadline := time.Now().Add(4 * time.Second)
	for len(processSlots) != before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(processSlots) != before {
		t.Fatal("confirmed signal did not release ownership")
	}
}
