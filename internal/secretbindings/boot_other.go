//go:build !linux && !darwin

package secretbindings

import "errors"

func bootIdentity() (string, error) { return "", errors.New("verified boot identity unavailable") }
