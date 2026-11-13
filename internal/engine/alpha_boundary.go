package engine

import (
	"context"
	"github.com/umayr/brine/internal/target"
	"io"
)

// AlphaReadOnly preserves pinned SDK observations but denies unqualified SDK
// writes, including stop and image force-remove. Stack label cleanup uses Dill.
type AlphaReadOnly struct{ Engine }

func (*AlphaReadOnly) StartContainer(context.Context, string) error {
	return target.ErrNegotiationUnavailable
}
func (*AlphaReadOnly) StopContainer(context.Context, string) error {
	return target.ErrNegotiationUnavailable
}
func (*AlphaReadOnly) RestartContainer(context.Context, string) error {
	return target.ErrNegotiationUnavailable
}
func (*AlphaReadOnly) PullImage(context.Context, string) (io.ReadCloser, error) {
	return nil, target.ErrNegotiationUnavailable
}
func (*AlphaReadOnly) RemoveImage(context.Context, string, bool) error {
	return target.ErrNegotiationUnavailable
}
