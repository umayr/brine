package web

import (
	"net/http"
)

// isHTMX reports whether the request was initiated by HTMX.
func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}
