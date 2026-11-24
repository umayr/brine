//go:build linux || darwin

package dill

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

var processSlots = make(chan struct{}, 8)

func configureProcessCancellation(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
func runSupervised(ctx context.Context, cmd *exec.Cmd) error {
	return runOwnedProcess(ctx, cmd, func(pid int) error { return groupCleanupError(pid, syscall.Kill(-pid, syscall.SIGKILL)) }, processExited)
}
func runOwnedProcess(ctx context.Context, cmd *exec.Cmd, killGroup func(int) error, exited func(int) (bool, error)) error {
	return runOwnedProcessWith(ctx, cmd, killGroup, exited, func() error { return cmd.Process.Kill() })
}

func runOwnedProcessWith(ctx context.Context, cmd *exec.Cmd, killGroup func(int) error, exited func(int) (bool, error), killLeader func() error) error {
	select {
	case processSlots <- struct{}{}:
	default:
		return errors.New("local process supervisor capacity exhausted")
	}
	retained := false
	defer func() {
		if !retained {
			<-processSlots
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	pipes, err := ownProcessIO(cmd)
	if err != nil {
		return err
	}
	cmd.Cancel = nil
	cmd.WaitDelay = 0 // no independent exec watcher or pipe goroutines
	if err = cmd.Start(); err != nil {
		pipes.finish(false)
		return err
	}
	pipes.started()
	pid := cmd.Process.Pid
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var observation error
	for {
		done, e := exited(pid)
		if e != nil {
			observation = e
			break
		}
		if done || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
	cleanup := killGroup(pid)
	if cleanup != nil {
		_ = killLeader()
	} // direct leader fallback, still before reap
	deadline := time.Now().Add(time.Second)
	for {
		done, e := exited(pid)
		if e == nil && done {
			wait := cmd.Wait() // no group signals occur after this point
			pipeErr := pipes.finish(true)
			if cleanup != nil || observation != nil {
				return errors.Join(ErrProcessUnknown, cleanup, observation, wait, pipeErr, ctx.Err())
			}
			return errors.Join(wait, pipeErr, ctx.Err())
		}
		if time.Now().After(deadline) {
			break
		}
		<-ticker.C
	}
	pipes.finish(false) // join all output before returning buffers/handler ownership
	retained = true
	// Capacity is reserved before Start. At most eight owned leaders can remain;
	// durable quarantine is separate and is never automatically cleared here.
	go func() {
		defer func() { <-processSlots }()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for range tick.C {
			done, e := exited(pid)
			if e != nil {
				continue
			}
			_ = killGroup(pid)
			if done {
				_ = cmd.Wait()
				return
			}
		}
	}()
	return errors.Join(ErrProcessUnknown, cleanup, observation, ctx.Err())
}
