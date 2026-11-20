package engine

import (
	"context"
	"errors"
)

var ErrCheckedUpRequired = errors.New("container cannot be started or restarted directly; use checked stack up")

var ErrManagedStart = errors.New("Dill-associated containers cannot be started directly; review the stack's supported lifecycle operations")

var ErrManagedRestart = errors.New("Dill-associated containers cannot be restarted directly; review the stack's supported lifecycle operations")

// LifecycleGuard blocks direct start/restart for Dill-associated containers and
// consults the trusted deployment record even if its container label vanished.
// A nil record lookup means no Brine secret state has been configured.
type LifecycleGuard struct {
	Engine
	SecretEnabled func(string) (bool, error)
	LockStack     func(string) func()
}

func dillAssociated(c *ContainerInfo) bool {
	for _, key := range []string{"dill.managed", "dill.stack", "dill.service", "dill.secret-enabled"} {
		if _, exists := c.Labels[key]; exists {
			return true
		}
	}
	return c.Stack != ""
}

func (g *LifecycleGuard) inspectStart(ctx context.Context, id, expectedStack string, restart bool) (string, error) {
	c, err := g.Engine.InspectContainer(ctx, id)
	if err != nil || c == nil || c.ID == "" {
		return "", ErrCheckedUpRequired
	}
	if restart && dillAssociated(c) {
		return "", ErrManagedRestart
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
	if !restart && dillAssociated(c) {
		return "", ErrManagedStart
	}
	return c.ID, nil
}
func (g *LifecycleGuard) change(ctx context.Context, id string, restart bool) error {
	first, err := g.Engine.InspectContainer(ctx, id)
	if err != nil || first == nil || first.ID == "" {
		return ErrCheckedUpRequired
	}
	if restart && dillAssociated(first) {
		if _, secret := first.Labels["dill.secret-enabled"]; secret {
			return ErrCheckedUpRequired
		}
		return ErrManagedRestart
	}
	stack := first.Labels["dill.stack"]
	if stack != "" && g.LockStack != nil {
		defer g.LockStack(stack)()
	}
	immutable, err := g.inspectStart(ctx, first.ID, stack, restart)
	if err != nil {
		return err
	}
	if immutable != first.ID {
		return ErrCheckedUpRequired
	}
	if restart {
		return g.Engine.RestartContainer(ctx, immutable)
	}
	if dillAssociated(first) {
		return ErrManagedStart
	}
	return g.Engine.StartContainer(ctx, immutable)
}
func (g *LifecycleGuard) StartContainer(ctx context.Context, id string) error {
	return g.change(ctx, id, false)
}
func (g *LifecycleGuard) RestartContainer(ctx context.Context, id string) error {
	return g.change(ctx, id, true)
}
