//go:build !linux && !darwin

package dill

import (
	"context"
	"os/exec"
)

// On other platforms retain CommandContext's direct-process cancellation;
// descendant termination is not guaranteed.
func configureProcessCancellation(cmd *exec.Cmd) {}

func runSupervised(_ context.Context, cmd *exec.Cmd) error { return cmd.Run() }
