//go:build linux || darwin

package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

// Clone gets a process group of its own so cancellation also stops Git's
// ordinary helpers. This does not sandbox helpers or change Git authority.
func (r *Runner) runClone(ctx context.Context, args ...string) error {
	cmd := r.command(ctx, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	// Clone diagnostics can include unbounded helper output or credentials.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
			err = errors.Join(ctxErr, err)
		}
		return fmt.Errorf("git clone: %w", err)
	}
	return nil
}
