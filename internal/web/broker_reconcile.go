package web

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/brokerapp"
	"github.com/umayr/brine/internal/secretbroker"
	"net/http"
)

func (h *Handler) brokerReconcile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.broker.Enabled() || h.brokerController == nil || h.brokerSession(r) == "" {
		http.Error(w, "Broker unavailable", 403)
		return
	}
	if !brokerForm(r, w, "request", "registration") {
		http.Error(w, "Invalid lookup", 400)
		return
	}
	name := chi.URLParam(r, "name")
	unlock, ok := h.admitStack(w, r, name)
	if !ok {
		return
	}
	defer unlock()
	if _, err := h.store.Get(name); err != nil {
		http.Error(w, "Stack unavailable", 404)
		return
	}
	var scope secretbroker.Scope
	for _, s := range h.broker.Scopes(name) {
		if s.RegistrationID == r.PostForm.Get("registration") {
			scope = s
		}
	}
	if scope.RegistrationID == "" {
		http.Error(w, "Scope unavailable", 403)
		return
	}
	records, err := h.broker.State.Deployments(scope)
	if err != nil {
		http.Error(w, "History unavailable", 409)
		return
	}
	var record secretbroker.DeploymentRecord
	for _, v := range records {
		if v.Intent.ID == r.PostForm.Get("request") {
			record = v
		}
	}
	if record.Intent.ID == "" {
		http.Error(w, "Request unavailable", 404)
		return
	}
	ctx, release, ok := h.auth.SubmissionLease(r)
	if !ok {
		http.Error(w, "Session unavailable", 403)
		return
	}
	defer release()
	target, closeTarget, err := h.openBrokerTarget(ctx, scope.TargetRegistration, func(c context.Context) bool { return c.Err() == nil && h.brokerSession(r) != "" })
	if err != nil {
		http.Error(w, "Approved target unavailable", 409)
		return
	}
	defer closeTarget()
	event, err := h.brokerController.Reconcile(ctx, record, target, func(c context.Context, s secretbroker.Scope) error {
		if c.Err() != nil || s != scope {
			return brokerapp.ErrUnavailable
		}
		return nil
	})
	if err != nil {
		h.renderAlert(w, "warning", "Original status is unavailable; no outcome was inferred and no deployment was resubmitted.")
		return
	}
	h.renderAlert(w, "info", "Original request: "+event.State+"; reported effects: "+event.Effects+". Reload History to inspect immutable evidence.")
}
