//go:build !linux && !darwin

package store

import "errors"

func publishRecipes(_, _ string) error {
	return errors.New("atomic recipe publication is unsupported on this platform; provision an existing library or use Linux or macOS")
}
