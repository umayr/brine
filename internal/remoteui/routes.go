package remoteui

import (
	"context"
	"errors"
	"net/http"
)

type requireWriteDeadlineKey struct{}

// ProtectedRoutes dispatches directly to exactly one protected handler. Mount
// this outside the server's global authentication middleware so that each
// handler's body limit runs before its single authentication/CSRF middleware.
// This constructor does not enable a server route or validate a backend.
func ProtectedRoutes(reads *Handler, enrollment *EnrollmentHandler) (http.Handler, error) {
	if reads == nil || enrollment == nil || reads.auth == nil || reads.auth != enrollment.auth || !reads.auth.Enabled() {
		return nil, errors.New("remote routes require the same enabled authenticator")
	}
	if _, ok := reads.backend.(ReadLeaseBackend); !ok {
		return nil, errors.New("remote routes require a request-scoped registry lease")
	}
	r, e := reads.Protected(), enrollment.Protected()
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/remote/reveal":
			w.Header().Set("Cache-Control", "no-store")
			http.NotFound(w, req)
		case "/remote/enrollment", "/remote/enrollment/review", "/remote/enrollment/approve":
			e.ServeHTTP(w, req)
		default:
			req = req.WithContext(context.WithValue(req.Context(), requireWriteDeadlineKey{}, true))
			r.ServeHTTP(w, req)
		}
	}), nil
}
