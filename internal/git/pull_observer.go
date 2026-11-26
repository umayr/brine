package git

import "context"

// PullObserver records source synchronization, not deployment or source content.
// It never supplies credentials or changes the Git runner's safety policy.
type PullObserver interface {
	Started() error
	Finished(bool, error)
}
type PullObserverFactory func(context.Context, string) (PullObserver, error)

type pullObserverContextKey struct{}

// WithPullObserver attaches one already accepted request journal. It changes no
// Git authorization or safety checks and prevents a duplicate global observer.
func WithPullObserver(ctx context.Context, observer PullObserver) context.Context {
	return context.WithValue(ctx, pullObserverContextKey{}, observer)
}
