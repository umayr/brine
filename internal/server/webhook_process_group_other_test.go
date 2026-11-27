//go:build !unix

package server

import "os/exec"

// The native runtime fixture is Linux-only; keep other package tests buildable.
func isolateFixtureProcess(c *exec.Cmd) {}
func killFixtureGroup(c *exec.Cmd)      { _ = c.Process.Kill() }
