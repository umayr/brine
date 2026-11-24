//go:build linux || darwin

package dill

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureProcessCancellation owns a new process group for this command. It
// must be called before Start, on a command created with exec.CommandContext.
func configureProcessCancellation(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pgid = 0
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
