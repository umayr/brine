package server

import (
	"context"
	"github.com/umayr/brine/internal/brokerapp"
	"github.com/umayr/brine/internal/dill"
)

// Runner gates receive a Pkl path, while broker protection is registered by
// logical stack name. Resolve through the existing ownership mapping; never
// guess a name from a basename or pass a path as an unregistered stack.
func installBrokerLegacyGates(runner *dill.Runner, broker *brokerapp.Service) {
	if runner == nil || broker == nil {
		return
	}
	unprotected := func(path string) error {
		if runner.ResolveStack == nil {
			return brokerapp.ErrUnavailable
		}
		name, err := runner.ResolveStack(path)
		if err != nil || name == "" {
			return brokerapp.ErrUnavailable
		}
		return broker.RequireUnprotected(name)
	}
	oldAction, oldValidate := runner.BeforeLegacyAction, runner.BeforeLegacyValidate
	runner.BeforeLegacyAction = func(ctx context.Context, path string) error {
		if err := unprotected(path); err != nil {
			return err
		}
		if oldAction != nil {
			return oldAction(ctx, path)
		}
		return nil
	}
	runner.BeforeLegacyValidate = func(ctx context.Context, path string) error {
		if err := unprotected(path); err != nil {
			return err
		}
		if oldValidate != nil {
			return oldValidate(ctx, path)
		}
		return nil
	}
}
