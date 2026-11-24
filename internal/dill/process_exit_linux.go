//go:build linux

package dill

import "golang.org/x/sys/unix"

func processExited(pid int) (bool, error) {
	var info unix.Siginfo
	err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil)
	if err == unix.EINTR {
		return false, nil
	}
	return info.Signo != 0, err
}
