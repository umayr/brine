package remoteui

import (
	"context"
	"github.com/umayr/brine/internal/targetregistry"
)

type testDeploymentCredentials string

func testCredentialRevision(h *Deployment, row targetregistry.Record) string {
	_, scope, lease, e := h.registry.AcquireHistoryRead(context.Background(), row.ID)
	if e != nil {
		return ""
	}
	defer lease.Close()
	return h.deploymentCredentialRevision(context.WithValue(context.Background(), deploymentEnrollmentKey{}, scope), row)
}
func (c testDeploymentCredentials) Credential(context.Context, targetregistry.Record) (string, error) {
	return string(c), nil
}
