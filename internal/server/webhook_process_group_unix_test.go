//go:build unix

package server

import (
	"os/exec"
	"syscall"
)

// An isolated group lets fixture teardown stop owned runtime children before
// observing cleanup, including when a transport failure left work in flight.
func isolateFixtureProcess(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func killFixtureGroup(c *exec.Cmd)      { _ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
