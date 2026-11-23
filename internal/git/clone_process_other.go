//go:build !linux && !darwin

package git

import (
	"context"
	"errors"
)

func (r *Runner) runClone(ctx context.Context, args ...string) error {
	return errors.New("bounded Git clone is unsupported on this platform")
}
