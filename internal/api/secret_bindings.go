//go:build unix

package api

import (
	"github.com/umayr/brine/internal/auth"
	"net/http"
)

// Keep old clients from mistaking a retired write workflow for success.
func retiredSecretBindings(w http.ResponseWriter, r *http.Request) {
	if !auth.Authenticated(r) {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	writeError(w, http.StatusServiceUnavailable, "strict secret functionality is disabled")
}
