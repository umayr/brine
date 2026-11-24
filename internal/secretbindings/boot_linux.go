//go:build linux

package secretbindings

import (
	"errors"
	"os"
	"strings"
)

func bootIdentity() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if len(s) != 36 {
		return "", errors.New("invalid host boot identity")
	}
	return s, nil
}
