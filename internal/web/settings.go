package web

import (
	"net/http"

	"github.com/umayr/brine/internal/auth"
)

// settingsAppearance exposes browser-local preferences, not server configuration.
// Rendering this page never queries the engine or evaluates stack source.
func (h *Handler) settingsAppearance(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "settings/appearance.html", &pageData{Page: "settings"})
}

func (h *Handler) settingsLiveUpdates(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "settings/live-updates.html", &pageData{Page: "settings"})
}

type sourceHistorySettingsData struct {
	pageData
	Available, Enabled bool
	Message            string
}

func (h *Handler) settingsSourceHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	data := &sourceHistorySettingsData{pageData: pageData{Page: "settings"}}
	switch {
	case h.sourceHistory == nil:
		data.Message = "Configure a private source archive before changing this default."
	case !auth.CookieAuthenticated(r):
		data.Message = "Sign in through the browser to change this default."
	default:
		r, end, ok := h.historySession(w, r, false)
		if !ok {
			return
		}
		defer end()
		enabled, err := h.sourceHistory.workspace.SourceCheckpointDefault()
		if r.Context().Err() != nil {
			http.Error(w, "Session or request expired", 403)
			return
		}
		if err != nil {
			data.Message = "The stored default is unavailable. No setting was assumed."
		} else {
			data.Available, data.Enabled = true, enabled
		}
	}
	h.render(w, r, "settings/source-history.html", data)
}

func (h *Handler) saveSourceHistorySettings(w http.ResponseWriter, r *http.Request) {
	r, end, ok := h.historySession(w, r, true)
	if !ok {
		return
	}
	defer end()
	if h.sourceHistory == nil {
		http.Error(w, "Source history is not enabled", 403)
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
	if h.sourceHistory.workspace.SetSourceCheckpointDefault(r.Context(), value == "yes") != nil {
		http.Error(w, "Default not confirmed. Open Source History settings to check its current state before retrying.", 409)
		return
	}
	http.Redirect(w, r, "/settings/source-history", http.StatusSeeOther)
}
