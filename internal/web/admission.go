package web

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Admission is cancellable; only admitted operations may detach execution.
func (h *Handler) admitStack(w http.ResponseWriter, r *http.Request, name string) (func(), bool) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if h.agent == nil || h.agent.Dill() == nil {
		if ctx.Err() != nil {
			http.Error(w, "Request cancelled before admission", http.StatusRequestTimeout)
			return nil, false
		}
		return func() {}, true
	}
	unlock, err := h.agent.Dill().LockStackContext(ctx, name)
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			http.Error(w, "Local admission blocked; inspect process quarantine and persistent state; no operation started", http.StatusServiceUnavailable)
			return nil, false
		}
		http.Error(w, "Stack busy or request cancelled; no operation started", http.StatusRequestTimeout)
		return nil, false
	}
	return unlock, true
}
