package web

import "net/http"

// settingsAppearance exposes browser-local preferences, not server configuration.
// Rendering this page never queries the engine or evaluates stack source.
func (h *Handler) settingsAppearance(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "settings/appearance.html", &pageData{Page: "settings"})
}
