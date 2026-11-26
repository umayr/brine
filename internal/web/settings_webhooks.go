package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/webhooks"
)

// EnableWebhooks is called once at startup, before HTTP serving begins.
func (h *Handler) EnableWebhooks(s *webhooks.Service, d webhooks.TriggerDispatcher) {
	h.webhooks = s
	h.webhookDispatcher = d
}

type WebhookOption struct {
	StackID    string   `json:"stack_id"`
	StackName  string   `json:"stack_name"`
	TargetID   string   `json:"target_id"`
	TargetName string   `json:"target_name"`
	Actions    []string `json:"actions"`
}
type WebhookOptions interface {
	WebhookOptions(context.Context) ([]WebhookOption, error)
}

func hooksReply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	if code != 204 {
		json.NewEncoder(w).Encode(v)
	}
}
func hooksError(w http.ResponseWriter, e error) {
	code := 503
	message := "Webhook storage or execution admission is unavailable"
	switch {
	case errors.Is(e, webhooks.ErrInvalid):
		code = 400
		message = "Invalid webhook configuration"
	case errors.Is(e, webhooks.ErrNotFound):
		code = 404
		message = "Webhook record not found"
	case errors.Is(e, webhooks.ErrConflict):
		code = 409
		message = "The record changed or this action is no longer permitted. Refresh and review again."
	case errors.Is(e, webhooks.ErrCapacity):
		message = "Webhook capacity is exhausted"
	case errors.Is(e, webhooks.ErrUnsupported):
		code = 403
		message = "This action is unavailable for the selected stack and target"
	}
	hooksReply(w, code, map[string]string{"error": message})
}
func hooksDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		hooksReply(w, 415, map[string]string{"error": "Expected application/json"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		hooksReply(w, 400, map[string]string{"error": "Invalid JSON request"})
		return false
	}
	return true
}
func (h *Handler) hooksGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !auth.Authenticated(r) {
			hooksReply(w, 401, map[string]string{"error": "Authentication is required"})
			return
		}
		if r.Method != http.MethodGet && !auth.CookieAuthenticated(r) {
			hooksReply(w, 403, map[string]string{"error": "Sign in with a browser session to manage webhooks"})
			return
		}
		if r.Method != http.MethodGet {
			if h.auth == nil {
				hooksReply(w, 403, map[string]string{"error": "Browser session required"})
				return
			}
			ctx, release, ok := h.auth.SubmissionLease(r)
			if !ok {
				hooksReply(w, 403, map[string]string{"error": "Browser session changed; sign in again"})
				return
			}
			defer release()
			r = r.WithContext(ctx)
		}
		if h.webhooks == nil {
			hooksReply(w, 503, map[string]string{"error": "Webhooks are not configured. Provision an encryption key and enable webhooks in the server configuration."})
			return
		}
		next(w, r)
	}
}
func (h *Handler) mountWebhooks(r chi.Router) {
	r.Get("/settings/webhooks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h.render(w, r, "settings/webhooks.html", &pageData{Page: "settings"})
	})
	r.Route("/api/webhooks", func(r chi.Router) {
		r.Get("/", h.hooksGuard(h.hooksInventory))
		r.Post("/endpoints", h.hooksGuard(h.hooksSaveEndpoint))
		r.Put("/endpoints/{id}", h.hooksGuard(h.hooksSaveEndpoint))
		r.Delete("/endpoints/{id}", h.hooksGuard(h.hooksEndpointAction))
		r.Post("/endpoints/{id}/rotate", h.hooksGuard(h.hooksEndpointAction))
		r.Post("/endpoints/{id}/test", h.hooksGuard(h.hooksEndpointAction))
		r.Post("/triggers", h.hooksGuard(h.hooksSaveTrigger))
		r.Put("/triggers/{id}", h.hooksGuard(h.hooksSaveTrigger))
		r.Delete("/triggers/{id}", h.hooksGuard(h.hooksTriggerAction))
		r.Post("/triggers/{id}/rotate", h.hooksGuard(h.hooksTriggerAction))
		r.Get("/deliveries/{id}/attempts", h.hooksGuard(func(w http.ResponseWriter, r *http.Request) {
			v, e := h.webhooks.Attempts(r.Context(), chi.URLParam(r, "id"))
			if e != nil {
				hooksError(w, e)
				return
			}
			hooksReply(w, 200, map[string]any{"attempts": v})
		}))
		r.Post("/deliveries/{id}/redeliver", h.hooksGuard(func(w http.ResponseWriter, r *http.Request) {
			if !hooksDecode(w, r, &struct{}{}) {
				return
			}
			if e := h.webhooks.Redeliver(r.Context(), chi.URLParam(r, "id")); e != nil {
				hooksError(w, e)
				return
			}
			hooksReply(w, 202, map[string]any{})
		}))
		r.Get("/requests", h.hooksGuard(func(w http.ResponseWriter, r *http.Request) {
			v, e := h.webhooks.Requests(r.Context(), r.URL.Query().Get("trigger_id"))
			if e != nil {
				hooksError(w, e)
				return
			}
			hooksReply(w, 200, map[string]any{"requests": v})
		}))
		r.Post("/requests/{id}/resolve", h.hooksGuard(func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				ExpectedUpdated time.Time `json:"expected_updated"`
				Inspected       bool      `json:"inspected"`
			}
			if !hooksDecode(w, r, &input) {
				return
			}
			if !input.Inspected || input.ExpectedUpdated.IsZero() {
				hooksError(w, webhooks.ErrInvalid)
				return
			}
			if e := h.webhooks.ResolveRequest(r.Context(), chi.URLParam(r, "id"), input.ExpectedUpdated); e != nil {
				hooksError(w, e)
				return
			}
			hooksReply(w, 200, map[string]any{})
		}))
	})
}
func (h *Handler) hooksInventory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	endpoints, e := h.webhooks.Endpoints(ctx)
	if e != nil {
		hooksError(w, e)
		return
	}
	triggers, e := h.webhooks.Triggers(ctx)
	if e != nil {
		hooksError(w, e)
		return
	}
	deliveries, e := h.webhooks.Deliveries(ctx, r.URL.Query().Get("endpoint_id"))
	if e != nil {
		hooksError(w, e)
		return
	}
	requests, e := h.webhooks.Requests(ctx, "")
	if e != nil {
		hooksError(w, e)
		return
	}
	options := []WebhookOption{}
	optionsAvailable := false
	if provider, ok := h.webhookDispatcher.(WebhookOptions); ok {
		options, e = provider.WebhookOptions(ctx)
		if e == nil {
			optionsAvailable = true
		} else {
			options = []WebhookOption{}
		}
		if options == nil {
			options = []WebhookOption{}
		}
	}
	hooksReply(w, 200, map[string]any{"endpoints": endpoints, "triggers": triggers, "deliveries": deliveries, "requests": requests, "catalog": webhooks.Catalog, "actions": webhooks.Actions, "options": options, "options_available": optionsAvailable, "can_manage": auth.CookieAuthenticated(r)})
}
func (h *Handler) hooksSaveEndpoint(w http.ResponseWriter, r *http.Request) {
	var ep webhooks.Endpoint
	if !hooksDecode(w, r, &ep) {
		return
	}
	id := chi.URLParam(r, "id")
	if id != ep.ID || (r.Method == http.MethodPost && ep.ID != "") {
		hooksError(w, webhooks.ErrInvalid)
		return
	}
	saved, key, e := h.webhooks.SaveEndpoint(r.Context(), ep)
	if e != nil {
		hooksError(w, e)
		return
	}
	code := 200
	if r.Method == http.MethodPost {
		code = 201
	}
	hooksReply(w, code, map[string]any{"endpoint": saved, "secret": key})
}
func (h *Handler) hooksEndpointAction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if r.Method == http.MethodDelete {
		var in struct {
			Version int64 `json:"version"`
		}
		if !hooksDecode(w, r, &in) {
			return
		}
		if e := h.webhooks.DeleteEndpoint(r.Context(), id, in.Version); e != nil {
			hooksError(w, e)
			return
		}
		hooksReply(w, 204, nil)
		return
	}
	if chi.RouteContext(r.Context()).RoutePattern() == "/api/webhooks/endpoints/{id}/rotate" {
		var in struct {
			Version int64 `json:"version"`
		}
		if !hooksDecode(w, r, &in) {
			return
		}
		key, e := h.webhooks.RotateSecret(r.Context(), id, in.Version)
		if e != nil {
			hooksError(w, e)
			return
		}
		hooksReply(w, 200, map[string]string{"secret": key})
		return
	}
	if !hooksDecode(w, r, &struct{}{}) {
		return
	}
	delivery, e := h.webhooks.Test(r.Context(), id)
	if e != nil {
		hooksError(w, e)
		return
	}
	hooksReply(w, 202, map[string]any{"delivery": delivery})
}
func (h *Handler) hooksSaveTrigger(w http.ResponseWriter, r *http.Request) {
	var t webhooks.Trigger
	if !hooksDecode(w, r, &t) {
		return
	}
	if chi.URLParam(r, "id") != t.ID || (r.Method == http.MethodPost && t.ID != "") {
		hooksError(w, webhooks.ErrInvalid)
		return
	}
	if t.Enabled {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if h.webhookDispatcher == nil || h.webhookDispatcher.Validate(ctx, t) != nil {
			hooksError(w, webhooks.ErrUnsupported)
			return
		}
	}
	saved, key, e := h.webhooks.SaveTrigger(r.Context(), t)
	if e != nil {
		hooksError(w, e)
		return
	}
	code := 200
	if r.Method == http.MethodPost {
		code = 201
	}
	hooksReply(w, code, map[string]any{"trigger": saved, "secret": key})
}
func (h *Handler) hooksTriggerAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version int64 `json:"version"`
	}
	if !hooksDecode(w, r, &in) {
		return
	}
	id := chi.URLParam(r, "id")
	if r.Method == http.MethodDelete {
		if e := h.webhooks.DeleteTrigger(r.Context(), id, in.Version); e != nil {
			hooksError(w, e)
			return
		}
		hooksReply(w, 204, nil)
		return
	}
	key, e := h.webhooks.RotateTriggerSecret(r.Context(), id, in.Version)
	if e != nil {
		hooksError(w, e)
		return
	}
	hooksReply(w, 200, map[string]string{"secret": key})
}
