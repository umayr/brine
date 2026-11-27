package secretbroker

import (
	"context"
)

// Policy callbacks must not reenter policy mutation or lifecycle operations.
func (m *Manager) authorized(ctx context.Context, i ReviewIdentity, b []Binding) error {
	m.policy.RLock()
	defer m.policy.RUnlock()
	return m.authorize(ctx, i, b)
}
