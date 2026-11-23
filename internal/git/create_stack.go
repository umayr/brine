package git

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/umayr/brine/internal/store"
)

const defaultCreateTimeout = 5 * time.Minute

type attemptStore interface {
	CreateGitAttempt(context.Context, string, *store.GitSource, string, func(context.Context, string) error) (*store.Stack, error)
}

// CreateContext bounds the entire Git creation attempt, including lock wait.
func CreateContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = defaultCreateTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// CreateStack uses the caller's bounded creation context.
func CreateStack(ctx context.Context, st store.StackStore, runner *Runner, name string, src *store.GitSource, entrypoint string) (*store.Stack, error) {
	attempts, ok := st.(attemptStore)
	if !ok || runner == nil {
		return nil, errors.New("Git stack creation unavailable")
	}
	return attempts.CreateGitAttempt(ctx, name, src, entrypoint, func(ctx context.Context, dest string) error {
		if err := runner.Clone(ctx, src.URL, src.Branch, dest); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("Git clone failed")
		}
		return nil
	})
}
