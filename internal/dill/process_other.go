//go:build !linux && !darwin

package dill

import "os/exec"

// On other platforms retain CommandContext's direct-process cancellation;
// descendant termination is not guaranteed.
func configureProcessCancellation(cmd *exec.Cmd) {}
