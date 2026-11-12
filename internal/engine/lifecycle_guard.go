package engine

import (
	"context"
	"errors"
)

var ErrCheckedUpRequired = errors.New("container cannot be started or restarted directly; use checked stack up")

// LifecycleGuard blocks direct start/restart for secret-enabled containers and
// consults the trusted deployment record even if its container label vanished.
// A nil record lookup means no Brine secret state has been configured.
type LifecycleGuard struct {
	Engine
	SecretEnabled func(string) (bool, error)
	LockStack     func(string) func()
}

func (g *LifecycleGuard) inspectStart(ctx context.Context, id, expectedStack string) (string, error) {
	c, err := g.Engine.InspectContainer(ctx, id)
	if err != nil || c == nil || c.ID == "" {
		return "", ErrCheckedUpRequired
	}
	labels := c.Labels
	if _, exists := labels["dill.secret-enabled"]; exists {
		return "", ErrCheckedUpRequired
	}
	managed, hasManaged := labels["dill.managed"]
	stack, hasStack := labels["dill.stack"]
	if stack != expectedStack {
		return "", ErrCheckedUpRequired
	}
	service, hasService := labels["dill.service"]
	if hasManaged || hasStack || hasService || c.Stack != "" {
		if managed != "true" || stack == "" || service == "" || c.Stack != stack {
			return "", ErrCheckedUpRequired
		}
		if g.SecretEnabled != nil {
			enabled, e := g.SecretEnabled(stack)
			if e != nil || enabled {
				return "", ErrCheckedUpRequired
			}
		}
	}
	return c.ID, nil
}
func (g *LifecycleGuard) change(ctx context.Context, id string, restart bool) error {
	first, err := g.Engine.InspectContainer(ctx, id)
	if err != nil || first == nil || first.ID == "" {
		return ErrCheckedUpRequired
	}
	stack := first.Labels["dill.stack"]
	if stack != "" && g.LockStack != nil {
		defer g.LockStack(stack)()
	}
	immutable, err := g.inspectStart(ctx, first.ID, stack)
	if err != nil || immutable != first.ID {
		return ErrCheckedUpRequired
	}
	if restart {
		return g.Engine.RestartContainer(ctx, immutable)
	}
	return g.Engine.StartContainer(ctx, immutable)
}
func (g *LifecycleGuard) StartContainer(ctx context.Context, id string) error {
	return g.change(ctx, id, false)
}
func (g *LifecycleGuard) RestartContainer(ctx context.Context, id string) error {
	return g.change(ctx, id, true)
}
