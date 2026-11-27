package remoteui

import (
	"context"
	"encoding/hex"
	"github.com/umayr/brine/internal/targetregistry"
)

// DeploymentBindingMetadata returns only non-secret custody bindings under a
// validated shared lease. It does not mint enrollment IDs or approve credentials.
func DeploymentBindingMetadata(ctx context.Context, r *targetregistry.Registry, id string) (out map[string]string, resultErr error) {
	if r == nil {
		return nil, errDeployCredential
	}
	row, scope, lease, e := r.AcquireHistoryRead(ctx, id)
	if e != nil {
		return nil, errDeployCredential
	}
	defer func() {
		if lease.Close() != nil {
			out, resultErr = nil, errDeployCredential
		}
	}()
	if row.HTTPSAddress == "" || scope == (targetregistry.HistoryIncarnation{}) {
		return nil, errDeployCredential
	}
	return map[string]string{"target_id": id, "binding": targetDigest(row), "enrollment": hex.EncodeToString(scope[:])}, nil
}
