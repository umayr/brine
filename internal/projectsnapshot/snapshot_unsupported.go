//go:build !linux && !darwin

package projectsnapshot

import "errors"

func build(string, string, Limits) (Snapshot, error) {
	return Snapshot{}, errors.New("project snapshot traversal is unsupported on this platform")
}
