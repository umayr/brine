package remoteui

import (
	"context"

	"github.com/umayr/brine/internal/remoteagent"
	"github.com/umayr/brine/internal/targetregistry"
)

// PinnedObserver opens a fresh authenticated session for each observation.
// It verifies the host and agent/engine binding but not the local key path or
// reveal-verifier fingerprint; those require independent operator verification.
type PinnedObserver struct{ Config remoteagent.Config }

func (o PinnedObserver) Observe(ctx context.Context, r targetregistry.Record) (targetregistry.Record, error) {
	return remoteagent.Observe(ctx, r, o.Config)
}
