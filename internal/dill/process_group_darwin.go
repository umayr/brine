//go:build darwin

package dill

import (
	"errors"
	"golang.org/x/sys/unix"
)

func groupCleanupError(pid int, err error) error {
	if err == nil || errors.Is(err, unix.ESRCH) {
		return nil
	}
	if !errors.Is(err, unix.EPERM) {
		return err
	}
	// XNU killpg1 excludes SZOMB members, returning EPERM when none were
	// signallable. Only accept that case after proving no live group members.
	// The owned leader remains unreaped throughout, preventing PGID reuse.
	members, inspectErr := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pid)
	if inspectErr != nil {
		return errors.Join(err, inspectErr)
	}
	for _, p := range members {
		if p.Proc.P_stat != 5 {
			return err
		}
	}
	return nil
}
