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
const checkpointUnconfirmed = "Source saved, but the automatic checkpoint could not be confirmed. Check History before retrying."

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
	if value != "yes" && value != "no" && value != "inherit" {
		http.Error(w, "Invalid checkpoint setting", 400)
		return
	}
	var override *bool
	if value != "inherit" {
		enabled := value == "yes"
		override = &enabled
	}
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
	if !sameCheckpointOverride(st.AutoCheckpoint, override) {
		if err = h.captureSavedSource(r, st, true, override); err != nil {
			http.Error(w, "Auto-Checkpoint setting not confirmed. Check History, workspace eligibility and archive capacity before retrying.", 409)
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
func (h *Handler) BeginSourceSave(r *http.Request) (observe func(string) store.SourceCheckpointStatus, release func()) {
	var lease context.Context
	release = func() {}
	if h.sourceHistory != nil && auth.CookieAuthenticated(r) {
		ctx, end, ok := h.auth.SubmissionLease(r)
		if ok {
			lease, release = ctx, end
		}
	}
	return func(name string) store.SourceCheckpointStatus {
		st, err := h.store.Get(name)
		if err != nil || st == nil {
			if h.sourceHistory != nil {
				return store.CheckpointUnconfirmed
			}
			return ""
		}
		if st.Git != nil || st.RemoteGit != nil {
			return ""
		}
		enabled, err := h.effectiveCheckpoint(st)
		if err != nil {
			return store.CheckpointNotRetained
		}
		if !enabled {
			return ""
		}
		// Bearer/no-auth saves retain their existing permissions but do not
		// acquire archive-write authority via the automatic policy.
		if lease == nil {
			return store.CheckpointNotRetained
		}
		return checkpointStatus(h.captureSavedSource(r.WithContext(lease), st, false, nil))
	}, release
}

func checkpointStatus(err error) store.SourceCheckpointStatus {
	switch {
	case err == nil || errors.Is(err, store.ErrSourceCheckpointDisabled):
		return ""
	case errors.Is(err, store.ErrSourceUncertain), errors.Is(err, store.ErrSourceInterrupted):
		return store.CheckpointUnconfirmed
	default:
		return store.CheckpointNotRetained
	}
}

func checkpointWarning(status store.SourceCheckpointStatus) string {
	switch status {
	case store.CheckpointNotRetained:
		return checkpointMissed
	case store.CheckpointUnconfirmed:
		return checkpointUnconfirmed
	default:
		return ""
	}
}

func checkpointNotice(w http.ResponseWriter, status store.SourceCheckpointStatus, dest string) string {
	if status == "" {
		return dest
	}
	w.Header().Set(store.SourceCheckpointHeader, string(status))
	u, err := url.Parse(dest)
	if err != nil {
		return dest
	}
	q := u.Query()
	q.Set("checkpoint", string(status))
	u.RawQuery = q.Encode()
	return u.String()
}

func sameCheckpointOverride(a, b *bool) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func (h *Handler) effectiveCheckpoint(st *store.Stack) (bool, error) {
	if st.AutoCheckpoint != nil {
		return *st.AutoCheckpoint, nil
	}
	if workspace, ok := h.store.(store.RevisionWorkspace); ok {
		return workspace.SourceCheckpointDefault()
	}
	return false, nil
}

func (h *Handler) captureSavedSource(r *http.Request, st *store.Stack, setOverride bool, override *bool) error {
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
	checkpoint := !setOverride || override != nil && *override
	records := false
	if checkpoint {
		_, records, err = s.checkpointHead(v)
		if err != nil {
			return err
		}
	}
	if !setOverride && !records {
		return nil
	}
	actor := auth.Identity(r)
	if actor == "" {
		return errors.New("checkpoint actor unavailable")
	}
	commit := s.workspace.CommitSource
	if !setOverride {
		commit = s.workspace.CommitAutomaticCheckpoint
	}
	return commit(r.Context(), st.Name, v.Baseline, func(current store.SourceView) (store.SourceUpdate, error) {
		if err := h.agent.Dill().CheckSourceProtection(st.Name); err != nil {
			return store.SourceUpdate{}, store.ErrSourceUnavailable
		}
		update := store.SourceUpdate{SetAutoCheckpoint: setOverride, AutoCheckpoint: override}
		if !records {
			return update, nil
		}
		update, err := s.createUpdate(r.Context(), current, actor, "")
		update.SetAutoCheckpoint, update.AutoCheckpoint = setOverride, override
		return update, err
	})
}
