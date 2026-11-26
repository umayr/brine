package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/store"
)

const checkpointMissed = "Source saved, but the automatic checkpoint was not retained. Check History and archive capacity."
const checkpointHeader = "X-Brine-Source-Checkpoint"

func (h *Handler) stackAutoCheckpoint(w http.ResponseWriter, r *http.Request) {
	r, end, ok := h.historySession(w, r, true)
	if !ok {
		return
	}
	defer end()
	name := chi.URLParam(r, "name")
	if h.sourceHistory == nil || store.ValidateStackName(name) != nil {
		http.Error(w, "Source history is not enabled", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.ParseForm() != nil {
		http.Error(w, "Invalid checkpoint setting", 400)
		return
	}
	value := r.PostForm.Get("enabled")
	if value != "yes" && value != "no" {
		http.Error(w, "Invalid checkpoint setting", 400)
		return
	}
	enabled := value == "yes"
	unlock, ok := h.admitStack(w, r, name)
	if !ok {
		return
	}
	defer unlock()
	st, err := h.store.Get(name)
	if err != nil || st == nil {
		http.Error(w, "Stack unavailable", 409)
		return
	}
	if st.AutoCheckpoint != enabled {
		if err = h.captureSavedSource(r, st, &enabled); err != nil {
			http.Error(w, "Auto-checkpoint setting not changed. Check workspace eligibility, protection state and archive capacity.", 409)
			return
		}
	}
	dest := "/stacks/" + name + "?tab=history"
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", dest)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// BeginSourceSave takes the browser submission lease before acquiring stack
// admission (same order as manual checkpoint/restore). Invoke the observer
// only after a successful write, while still holding stack admission. If
// retention fails, the caller reports the gap without undoing the saved edit.
func (h *Handler) BeginSourceSave(r *http.Request) (observe func(string) bool, release func()) {
	var lease context.Context
	release = func() {}
	if h.sourceHistory != nil && auth.CookieAuthenticated(r) {
		ctx, end, ok := h.auth.SubmissionLease(r)
		if ok {
			lease, release = ctx, end
		}
	}
	return func(name string) bool {
		st, err := h.store.Get(name)
		if err != nil || st == nil || !st.AutoCheckpoint {
			return false
		}
		// Bearer/no-auth saves retain their existing permissions but do not
		// acquire archive-write authority via the automatic policy.
		if lease == nil {
			return true
		}
		return h.captureSavedSource(r.WithContext(lease), st, nil) != nil
	}, release
}

func (h *Handler) checkpointNotice(w http.ResponseWriter, missed bool, dest string) string {
	if !missed {
		return dest
	}
	w.Header().Set(checkpointHeader, "not-retained")
	u, err := url.Parse(dest)
	if err != nil {
		return dest
	}
	q := u.Query()
	q.Set("checkpoint", "not-retained")
	u.RawQuery = q.Encode()
	return u.String()
}

func (h *Handler) captureSavedSource(r *http.Request, st *store.Stack, setting *bool) error {
	if h.sourceHistory == nil || !auth.CookieAuthenticated(r) || r.Context().Err() != nil {
		return store.ErrSourceUnavailable
	}
	_, pending, err := h.manualRows(st.Name)
	if err != nil || !h.sourceEligible(r, st, pending) {
		return store.ErrSourceUnavailable
	}
	s := h.sourceHistory
	v, err := s.workspace.ReviewSource(r.Context(), st.Name)
	if err != nil {
		return err
	}
	checkpoint := setting == nil || *setting
	records := false
	if checkpoint {
		_, records, err = s.checkpointHead(v)
		if err != nil {
			return err
		}
	}
	if setting == nil && !records {
		return nil
	}
	actor := auth.Identity(r)
	if actor == "" {
		return errors.New("checkpoint actor unavailable")
	}
	return s.workspace.CommitSource(r.Context(), st.Name, v.Baseline, func(current store.SourceView) (store.SourceUpdate, error) {
		if err := h.agent.Dill().CheckSourceProtection(st.Name); err != nil {
			return store.SourceUpdate{}, store.ErrSourceUnavailable
		}
		update := store.SourceUpdate{AutoCheckpoint: setting}
		if !records {
			return update, nil
		}
		update, err := s.createUpdate(r.Context(), current, actor, "")
		update.AutoCheckpoint = setting
		return update, err
	})
}
