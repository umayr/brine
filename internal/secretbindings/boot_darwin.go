//go:build darwin

package secretbindings

import (
	"fmt"
	"golang.org/x/sys/unix"
)

func bootIdentity() (string, error) {
	v, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d", v.Sec, v.Usec), nil
}
