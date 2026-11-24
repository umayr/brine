package web

import (
	"context"
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
		http.Error(w, "Stack busy or request cancelled; no operation started", http.StatusRequestTimeout)
		return nil, false
	}
	return unlock, true
}
