package dill

import "context"

// LifecycleObserver is a control-plane journal, never execution authorization.
// The runner retains every source, protection, endpoint and ownership check.
// Factories are configured once before serving; nil observers are no-ops.
type LifecycleObserver interface {
	Started() error
	Finished(error)
}
type LifecycleObserverFactory func(context.Context, string, string) (LifecycleObserver, error)
type lifecycleObserverContextKey struct{}

func (r *Runner) observeLifecycle(ctx context.Context, path, action string) (context.Context, func(error), error) {
	if r.ObserveLifecycle == nil {
		return ctx, func(error) {}, nil
	}
	observer, e := r.ObserveLifecycle(ctx, path, action)
	if e != nil {
		return ctx, nil, e
	}
	if observer == nil {
		return ctx, func(error) {}, nil
	}
	return context.WithValue(ctx, lifecycleObserverContextKey{}, observer), observer.Finished, nil
}
func markLifecycleStarted(ctx context.Context) error {
	if observer, ok := ctx.Value(lifecycleObserverContextKey{}).(LifecycleObserver); ok {
		return observer.Started()
	}
	return nil
}
