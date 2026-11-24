package api

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Admission is cancellable; only admitted operations may detach execution.
func (h *stackHandler) admitStack(w http.ResponseWriter, r *http.Request, name string) (func(), bool) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if h.agent == nil || h.agent.Dill() == nil {
		if ctx.Err() != nil {
			writeError(w, http.StatusRequestTimeout, "Request cancelled before admission")
			return nil, false
		}
		return func() {}, true
	}
	unlock, err := h.agent.Dill().LockStackContext(ctx, name)
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			writeError(w, http.StatusServiceUnavailable, "Local admission blocked; inspect process quarantine and persistent state; no operation started")
			return nil, false
		}
		writeError(w, http.StatusRequestTimeout, "Stack busy or request cancelled; no operation started")
		return nil, false
	}
	return unlock, true
}
