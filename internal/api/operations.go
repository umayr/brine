package api

import (
	"github.com/umayr/brine/internal/auth"
	"net/http"
)

func (h *stackHandler) operationGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !auth.Authenticated(r) {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	writeError(w, http.StatusServiceUnavailable, "Alpha operation history is disabled")
}
