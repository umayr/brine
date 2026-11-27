package remoteui

import (
	"context"

	"net/http"
	"time"
)

// SetLegacyGuard is startup-only. A configured broker serializes ordinary
// submission against its permanent registrations; recovery reads stay available.
func (h *Deployment) SetLegacyGuard(fn func(context.Context, string, func()) error) {
	h.legacyGuard = fn
}

// automationUnprotected holds the broker's existing admission lock for the
// complete callback. Callers that execute must acquire gitAdmission first,
// matching ordinary Git polling. Recovery/status reads do not use this guard.
func automationUnprotected[T any](h *Deployment, ctx context.Context, stack string, fn func() (T, error)) (out T, err error) {
	if h.legacyGuard == nil {
		return fn()
	}
	guardErr := h.legacyGuard(ctx, stack, func() { out, err = fn() })
	if guardErr != nil {
		return out, ErrAutomationAdmission
	}
	return
}
func (h *Deployment) guardOrdinary(w http.ResponseWriter, r *http.Request, ctx context.Context, session string, fn func(http.ResponseWriter)) bool {
	if h.legacyGuard == nil || r.Method != http.MethodPost {
		return false
	}
	path := r.URL.Path
	form := r.PostForm
	if path == deployPath+"/recover" || path == deployPath+"/status" || path == deployPath+"/git" {
		return false
	}
	if path == deployPath+"/git-config" && form.Get("mode") != "enable" {
		return false
	}
	switch path {
	case deployPath + "/create", deployPath + "/review", deployPath + "/stage", deployPath + "/remove-preview", deployPath + "/git-config":
	default:
		return false
	}
	name := form.Get("stack")
	if path == deployPath+"/create" {
		h.mu.Lock()
		review, ok := h.pending[form.Get("id")]
		h.mu.Unlock()
		if !ok || review.Session != session || !time.Now().Before(review.Expiry) {
			http.Error(w, "Review unavailable", 409)
			return true
		}
		name = review.Stack
	}
	started := false
	response := &guardResponse{header: make(http.Header)}
	err := h.legacyGuard(ctx, name, func() { started = true; fn(response) })
	if err != nil || response.failed {
		if !started {
			http.Error(w, "Broker protection or state prevents ordinary remote deployment. Use the stack Plan; recovery remains available.", 409)
		} else {
			http.Error(w, "Submission outcome unavailable after admission. Inspect the original request and target journal; do not replay.", 503)
		}
		return true
	}
	for k, v := range response.header {
		w.Header()[k] = v
	}
	code := response.code
	if code == 0 {
		code = 200
	}
	w.WriteHeader(code)
	_, _ = w.Write(response.body.Bytes())
	return true
}
