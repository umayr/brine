package main

import (
	"context"
	"path/filepath"

	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/secretbindings"
	"github.com/umayr/brine/internal/store"
)

func stackPathResolver(st store.StackStore) func(string) (string, error) {
	return func(path string) (string, error) {
		stacks, err := st.List()
		if err != nil {
			return "", dill.ErrSecretUnavailable
		}
		name := ""
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return "", dill.ErrSecretUnavailable
		}
		for _, stack := range stacks {
			match := stack.PklPath == path
			if !match && filepath.Dir(stack.PklPath) == filepath.Dir(path) {
				_, err := st.ReadFile(stack.Name, filepath.Base(path))
				match = err == nil
			}
			if match {
				if name != "" {
					return "", dill.ErrSecretUnavailable
				}
				name = stack.Name
			}
		}
		if name == "" {
			return "", dill.ErrSecretUnavailable
		}
		return name, nil
	}
}

// legacySecretRecordGate protects diagnostics for previously secret-enabled
// deployments without requiring an engine for ordinary offline validation.
func legacySecretRecordGate(st store.StackStore, state *secretbindings.Store) func(context.Context, string) error {
	return func(_ context.Context, path string) error {
		name, err := stackPathResolver(st)(path)
		if err != nil {
			return dill.ErrSecretUnavailable
		}
		enabled, err := state.SecretEnabled(secretbindings.DeploymentID(name))
		if err != nil || enabled {
			return dill.ErrSecretUnavailable
		}
		return nil
	}
}

// legacySecretGate prevents a disabled or incompatible secret workflow from
// falling through to an unchecked legacy mutation. Stop/down are not gated.
func legacySecretGate(st store.StackStore, eng engine.Engine, state *secretbindings.Store) func(context.Context, string) error {
	return func(ctx context.Context, path string) error {
		name, err := stackPathResolver(st)(path)
		if err != nil {
			return dill.ErrSecretUnavailable
		}
		if state != nil {
			enabled, err := state.SecretEnabled(secretbindings.DeploymentID(name))
			if err != nil || enabled {
				return dill.ErrSecretUnavailable
			}
		}
		containers, err := eng.ListContainers(ctx)
		if err != nil {
			return dill.ErrSecretUnavailable
		}
		for _, entry := range containers {
			if entry.Stack != name && entry.Labels["dill.stack"] != name {
				continue
			}
			if entry.ID == "" {
				return dill.ErrSecretUnavailable
			}
			c, err := eng.InspectContainer(ctx, entry.ID)
			if err != nil || c == nil || c.ID != entry.ID {
				return dill.ErrSecretUnavailable
			}
			if _, ok := c.Labels["dill.secret-enabled"]; ok {
				return dill.ErrSecretUnavailable
			}
			if c.Labels["dill.managed"] != "true" || c.Labels["dill.stack"] != name || c.Labels["dill.service"] == "" {
				return dill.ErrSecretUnavailable
			}
		}
		return nil
	}
}
