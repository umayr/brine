//go:build linux || darwin

package dill

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

func configureProcessCancellation(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// Observe exit without reaping: the leader reserves its PID until group cleanup
// is finished. No cancellation goroutine may signal the numeric PGID after Wait.
func runSupervised(ctx context.Context, cmd *exec.Cmd) error {
	delay := cmd.WaitDelay
	cmd.Cancel = nil
	cmd.WaitDelay = 0 // disable exec's independent cancellation/reaping machinery
	if err := cmd.Start(); err != nil {
		return err
	}
	cmd.WaitDelay = delay // still bound inherited pipes after group cleanup
	var observation error
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		exited, err := processExited(cmd.Process.Pid)
		if err != nil {
			observation = err
			break
		}
		if exited || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
	killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	killErr = groupCleanupError(cmd.Process.Pid, killErr)
	if observation != nil {
		observation = fmt.Errorf("observe owned process: %w", observation)
	}
	if killErr != nil {
		killErr = fmt.Errorf("clean owned process group: %w", killErr)
	}
	waitErr := cmd.Wait()
	return errors.Join(observation, killErr, waitErr, ctx.Err())
}
