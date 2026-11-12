//go:build unix

package dill

import (
	"context"
	"encoding/json"
)

// Validate exposes only validity, never raw diagnostics, for strict-mode Pkl.
func (c *StrictController) Validate(ctx context.Context, path string, content *string) error {
	id, stack, enabled, err := c.context(ctx, path)
	if err != nil {
		return err
	}
	args := []string{"-f", path, "-p", stack, "--engine", c.Secret.Engine, "--engine-socket", c.Secret.Socket, "--secret-target", c.Secret.Target, "validate", "--format", "json"}
	if enabled {
		binding, err := c.Secret.State.BindingPath(id, stack, c.Secret.Target, false)
		if err != nil {
			return ErrSecretUnavailable
		}
		args = append(args, "--secret-bindings", binding)
	}
	if content != nil {
		args = append(args, "--stdin")
	}
	out, exit, err := c.Secret.captureInput(ctx, content, args...)
	if err != nil || exit != 0 {
		return ErrSecretUnavailable
	}
	m, err := strictObject(out, "valid")
	if err != nil {
		return ErrSecretUnavailable
	}
	var valid bool
	if string(m["valid"]) == "null" || json.Unmarshal(m["valid"], &valid) != nil || !valid {
		return ErrSecretUnavailable
	}
	return nil
}
