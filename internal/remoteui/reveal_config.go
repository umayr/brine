//go:build darwin || linux

package remoteui

import (
	"context"
	"errors"

	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/remoteagent"
	"github.com/umayr/brine/internal/remotereveal"
	"github.com/umayr/brine/internal/revealsigner"
	"github.com/umayr/brine/internal/targetregistry"
)

// OpenRevealHandler opens existing operator-provisioned key and audit state.
// It does not mount the returned handler or alter the existing dispatcher.
func OpenRevealHandler(ctx context.Context, a *auth.Authenticator, registry *targetregistry.Registry, auditRoot, keyPath string, transport remoteagent.Config) (*RevealHandler, error) {
	if ctx == nil || ctx.Err() != nil || a == nil || !a.Enabled() || registry == nil || auditRoot == "" || keyPath == "" {
		return nil, errors.New("reveal unavailable")
	}
	signer, err := revealsigner.Open(keyPath)
	if err != nil {
		return nil, errors.New("reveal unavailable")
	}
	executor, err := remotereveal.OpenConfigured(ctx, registry, auditRoot, signer, transport)
	if err != nil {
		return nil, errors.New("reveal unavailable")
	}
	return NewRevealHandler(a, registry, executor)
}
