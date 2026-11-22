//go:build !linux && !darwin

package store

import "errors"

func renameNoReplace(from, to string) error {
	return errors.New("exclusive stack publication is unsupported on this platform")
}
