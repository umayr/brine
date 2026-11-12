//go:build unix

package dill

import (
	"context"
	"path/filepath"

	"github.com/umayr/brine/internal/secretbindings"
)

// StrictController routes all plan/up/start/restart operations through exact
// discovery. Brine route and Git callers must hold Runner.LockStack throughout.
type StrictController struct {
	Secret     *SecretRunner
	Resolve    func(string) (string, error)
	LegacyGate func(context.Context, string) error
}

func (c *StrictController) context(ctx context.Context, path string) (string, string, bool, error) {
	if c == nil || c.Secret == nil || !c.Secret.valid() || c.Resolve == nil || !filepath.IsAbs(path) {
		return "", "", false, ErrSecretUnavailable
	}
	if err := c.Secret.Runner.checkSource(path); err != nil {
		return "", "", false, ErrSecretUnavailable
	}
	if err := c.Secret.Runner.CheckSecretCapabilities(ctx, c.Secret.Engine); err != nil {
		return "", "", false, err
	}
	stack, err := c.Resolve(path)
	if err != nil || stack == "" {
		return "", "", false, ErrSecretUnavailable
	}
	id := secretbindings.DeploymentID(stack)
	enabled, err := c.Secret.State.SecretEnabled(id)
	if err != nil {
		return "", "", false, ErrSecretUnavailable
	}
	return id, stack, enabled, nil
}
func (c *StrictController) Up(ctx context.Context, path string) error {
	id, stack, enabled, err := c.context(ctx, path)
	if err != nil {
		return err
	}
	if enabled {
		return c.Secret.applyActive(ctx, id, stack, path)
	}
	if c.LegacyGate == nil || c.LegacyGate(ctx, path) != nil {
		return ErrSecretUnavailable
	}
	_, exit, err := c.Secret.capture(ctx, "-f", path, "-p", stack, "--engine", c.Secret.Engine, "--engine-socket", c.Secret.Socket, "--secret-target", c.Secret.Target, "up")
	if err != nil || exit != 0 {
		return ErrSecretApplyFailed
	}
	return nil
}
func (c *StrictController) Plan(ctx context.Context, path string) (*PlanResult, error) {
	id, stack, enabled, err := c.context(ctx, path)
	if err != nil {
		return nil, err
	}
	if enabled {
		p, _, _, _, err := c.Secret.plan(ctx, id, stack, path, false)
		return p, err
	}
	if c.LegacyGate == nil || c.LegacyGate(ctx, path) != nil {
		return nil, ErrSecretUnavailable
	}
	data, exit, err := c.Secret.capture(ctx, "-f", path, "-p", stack, "--engine", c.Secret.Engine, "--engine-socket", c.Secret.Socket, "--secret-target", c.Secret.Target, "plan", "--format", "json")
	if err != nil {
		return nil, err
	}
	return parseSecretPlan(data, exit)
}
func (c *StrictController) Start(ctx context.Context, path string, restart bool) error {
	_, stack, enabled, err := c.context(ctx, path)
	if err != nil {
		return err
	}
	if enabled || c.LegacyGate == nil || c.LegacyGate(ctx, path) != nil {
		return ErrSecretUnavailable
	}
	command := "start"
	if restart {
		command = "restart"
	}
	_, exit, err := c.Secret.capture(ctx, "-f", path, "-p", stack, "--engine", c.Secret.Engine, "--engine-socket", c.Secret.Socket, "--secret-target", c.Secret.Target, command)
	if err != nil || exit != 0 {
		return ErrSecretUnavailable
	}
	return nil
}
