//go:build darwin

package dill

import (
	"fmt"
	"golang.org/x/sys/unix"
)

func processExited(pid int) (bool, error) {
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return false, err
	}
	if p == nil || int(p.Proc.P_pid) != pid {
		return false, fmt.Errorf("owned process status unavailable")
	}
	// sys/proc.h: SZOMB=5, awaiting collection by parent. No Wait occurs here.
	return p.Proc.P_stat == 5, nil
}
