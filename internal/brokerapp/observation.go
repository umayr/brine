package brokerapp

import (
	"context"
	"github.com/umayr/brine/internal/secretbroker"
)

// ExecutionMetadata deliberately excludes bindings, provider locators, values,
// prepared references and source bytes. Observation never grants authority.
type ExecutionMetadata struct{ Stack, Target, TargetKind, TargetDigest, Action, Request string }

func (c *Controller) executionMetadata(i secretbroker.ReviewIdentity, action, request string) ExecutionMetadata {
	kind := "unavailable"
	switch c.service.policy.Targets[i.Scope.TargetRegistration].Kind {
	case "local":
		kind = "local"
	case "https":
		kind = "remote"
	}
	return ExecutionMetadata{Stack: i.Scope.StackName, Target: i.Scope.TargetRegistration, TargetKind: kind, TargetDigest: i.TargetDigest, Action: action, Request: request}
}

type ExecutionObservation interface {
	Started() error
	Finished(Outcome, error, bool)
}
type ExecutionObserver func(context.Context, ExecutionMetadata) (ExecutionObservation, error)

// SetExecutionObserver is startup-only. Reconciliation/status never invokes it.
func (c *Controller) SetExecutionObserver(observer ExecutionObserver) { c.observer = observer }
func (c *Controller) observe(ctx context.Context, meta ExecutionMetadata) (ExecutionObservation, error) {
	if c.observer == nil {
		return nil, nil
	}
	o, err := c.observer(ctx, meta)
	if err != nil || o == nil {
		return nil, ErrUnavailable
	}
	return o, nil
}
