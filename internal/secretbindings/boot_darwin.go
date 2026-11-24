//go:build darwin

package secretbindings

import (
	"errors"
	"golang.org/x/sys/unix"
)

func bootIdentity() (string, error) {
	// Wall-clock boot time is not a safe epoch when the host clock changes.
	value, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return "", err
	}
	if len(value) != 36 {
		return "", errors.New("invalid host boot-session UUID")
	}
	return value, nil
}
