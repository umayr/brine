package remoteui

import (
	"context"
	"errors"
	"github.com/umayr/brine/internal/agentclient"
)

// ExecutionMetadata contains only non-secret integration bindings, never source,
// transport material or engine output. The observer grants no authority.
type ExecutionMetadata struct {
	Host, Target, Stack, Action, Request string
	RuntimeOnly                          bool
}
type ExecutionObservation interface {
	Started() error
	Finished(agentclient.DeploymentStatus, error, bool)
}
type ExecutionObserver func(context.Context, ExecutionMetadata) (ExecutionObservation, error)
type observedExecutionKey struct{}

// WithObservedExecution marks an incoming operation whose durable journal is
// already owned by its dispatcher. It is not an authentication/approval token.
func WithObservedExecution(ctx context.Context) context.Context {
	return context.WithValue(ctx, observedExecutionKey{}, true)
}
func (h *Deployment) SetExecutionObserver(observer ExecutionObserver) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.observeExecution = observer
}
func (h *Deployment) observeTarget(ctx context.Context, meta ExecutionMetadata, run func() (agentclient.DeploymentStatus, error)) (agentclient.DeploymentStatus, error) {
	h.mu.Lock()
	factory := h.observeExecution
	h.mu.Unlock()
	if factory == nil || ctx.Value(observedExecutionKey{}) != nil {
		return run()
	}
	o, e := factory(ctx, meta)
	if e != nil {
		return agentclient.DeploymentStatus{}, e
	}
	if o == nil {
		return agentclient.DeploymentStatus{}, errors.New("remote observation unavailable")
	}
	if e = o.Started(); e != nil {
		o.Finished(agentclient.DeploymentStatus{}, e, false)
		return agentclient.DeploymentStatus{}, e
	}
	status, e := run()
	o.Finished(status, e, true)
	return status, e
}
