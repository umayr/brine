//go:build darwin || linux

package remotereveal

import (
	"context"
	"errors"

	"github.com/umayr/brine/internal/remoteagent"
	"github.com/umayr/brine/internal/revealaudit"
	"github.com/umayr/brine/internal/targetregistry"
)

// OpenConfigured opens only existing operator-provisioned state. It never
// initializes the audit root or creates a key, and exposes no route itself.
func OpenConfigured(ctx context.Context, registry *targetregistry.Registry, auditRoot string, signer Signer, transport remoteagent.Config) (*Executor, error) {
	if ctx == nil || registry == nil || auditRoot == "" || signer == nil {
		return nil, errors.New("reveal unavailable")
	}
	audit, err := revealaudit.Open(ctx, auditRoot)
	if err != nil {
		return nil, errors.New("reveal unavailable")
	}
	return &Executor{Registry: registry, Signer: signer, Audit: audit, Open: PinnedOpener(transport)}, nil
}
