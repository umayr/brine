//go:build linux

package dill

import (
	"errors"
	"syscall"
)

func groupCleanupError(_ int, err error) error {
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
