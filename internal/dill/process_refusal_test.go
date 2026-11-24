//go:build linux || darwin

package dill

import (
	"context"
	"errors"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestBothSignalsRefusedReturnsUnknown(t *testing.T) {
	baseline := len(processSlots)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	// The synthetic process exits itself even when both injected signals fail.
	cmd := exec.CommandContext(ctx, "sleep", "2")
	configureProcessCancellation(cmd)
	leaderCalls := 0
	start := time.Now()
	var allowCleanup atomic.Bool
	err := runOwnedProcessWith(ctx, cmd, func(pid int) error {
		if !allowCleanup.Load() {
			return syscall.EPERM
		}
		return groupCleanupError(pid, syscall.Kill(-pid, syscall.SIGKILL))
	}, processExited, func() error { leaderCalls++; return syscall.EPERM })
	if !errors.Is(err, ErrProcessUnknown) || leaderCalls != 1 {
		t.Fatalf("result %v, fallback calls %d", err, leaderCalls)
	}
	if time.Since(start) > 1800*time.Millisecond {
		t.Fatal("refused signals blocked return")
	}
	if len(processSlots) != baseline+1 {
		t.Fatal("lost retained ownership")
	}
	// Leader exits naturally, but a refused group signal must still retain ownership.
	time.Sleep(2 * time.Second)
	if len(processSlots) != baseline+1 {
		t.Error("leader exit released refused group ownership")
	}
	allowCleanup.Store(true)
	deadline := time.Now().Add(4 * time.Second)
	for len(processSlots) != baseline && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(processSlots) != baseline {
		t.Fatal("eventual exit did not release slot")
	}
}
func TestSupervisorCapacityDeniesBeforeStart(t *testing.T) {
	if len(processSlots) != 0 {
		t.Fatal("another test retained supervisor")
	}
	for i := 0; i < cap(processSlots); i++ {
		processSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(processSlots); i++ {
			<-processSlots
		}
	}()
	cmd := exec.Command("sleep", "1")
	configureProcessCancellation(cmd)
	err := runSupervised(context.Background(), cmd)
	if err == nil || cmd.Process != nil {
		t.Fatalf("capacity allowed launch: %v", err)
	}
}
