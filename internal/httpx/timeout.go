// Package httpx holds HTTP helpers shared by the API and web packages.
package httpx

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// Timeouts configures the two request classes Brine serves.
type Timeouts struct {
	// Default applies to ordinary read/write handlers.
	Default time.Duration
	// Lifecycle applies to deploy/teardown/git operations, which are bounded
	// by the detached command timeout rather than the HTTP request.
	Lifecycle time.Duration
}

// Timeout returns chi's timeout middleware, or a pass-through when d <= 0.
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	if d <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return middleware.Timeout(d)
}

// Detached returns a context that survives client disconnects but still has a
// deadline. Use it for commands whose completion must not depend on the
// browser keeping the response open (deploys, teardowns, git operations).
func Detached(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	ctx := context.WithoutCancel(parent)
	if d <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}
