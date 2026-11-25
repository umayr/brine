package remoteui

import (
	"context"
	"net/http"

	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/targetregistry"
)

// NewRestartMetadataHandler exposes metadata only. It installs no deployment
// backend, credential source, content-read approval or provider client. The
// caller owns the store and must drain this handler before closing it.
func NewRestartMetadataHandler(ctx context.Context, a *auth.Authenticator, registry *targetregistry.Registry, archive *snapshothistory.Store, pins []config.RestartMetadataPin) (http.Handler, error) {
	if a == nil || !a.Enabled() || registry == nil || archive == nil || len(pins) == 0 {
		return nil, errRestartMetadata
	}
	approvals := make([]restartMetadataApproval, len(pins))
	for i, pin := range pins {
		approvals[i] = restartMetadataApproval{host: pin.Host, incarnation: pin.Incarnation}
	}
	access, err := newRestartMetadataAllowlist(ctx, registry, approvals)
	if err != nil {
		return nil, errRestartMetadata
	}
	h := &Deployment{auth: a, registry: registry}
	return h.protectedRestartMetadata(archive, access), nil
}
