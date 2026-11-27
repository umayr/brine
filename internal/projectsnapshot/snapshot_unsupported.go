//go:build !linux && !darwin

package projectsnapshot

import (
	"errors"
	"os"
)

func buildDirectory(*os.File, string, Limits) (Snapshot, error) {
	return Snapshot{}, errors.New("project snapshot traversal is unsupported on this platform")
}
func build(string, string, Limits) (Snapshot, error) {
	return Snapshot{}, errors.New("project snapshot traversal is unsupported on this platform")
}
