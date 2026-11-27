package web

import (
	"context"
	"github.com/umayr/brine/internal/brokerapp"
)

// Package-private transport seam for isolated HTTP tests; production always uses operator policy.
type brokerTargetOpener func(context.Context, string, func(context.Context) bool) (brokerapp.Target, func(), error)

func (h *Handler) openBrokerTarget(ctx context.Context, id string, current func(context.Context) bool) (brokerapp.Target, func(), error) {
	if h.brokerTargets != nil {
		return h.brokerTargets(ctx, id, current)
	}
	return h.broker.OpenTarget(ctx, id, current)
}
