package web

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/umayr/brine/internal/auth"
)

type generalSettingsData struct {
	pageData
	BrowserSession bool
	Managed        auth.ManagedFields
}

func (h *Handler) settingsGeneral(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	h.render(w, r, "settings/general.html", &generalSettingsData{
		pageData:       pageData{Page: "settings"},
		BrowserSession: auth.CookieAuthenticated(r),
		Managed:        h.auth.CredentialFields(),
	})
}

func (h *Handler) settingsCredentials(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	reply := func(status int, data any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(data)
	}
	fail := func(status int, message string) { reply(status, map[string]string{"error": message}) }
	if !auth.CookieAuthenticated(r) {
		fail(http.StatusForbidden, "Sign in with a browser session to manage credentials.")
		return
	}

	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(http.StatusUnsupportedMediaType, "Expected a JSON credential change.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var change auth.CredentialChange
	if decoder.Decode(&change) != nil || decoder.Decode(new(any)) != io.EOF {
		fail(http.StatusBadRequest, "Invalid credential change.")
		return
	}
	result, err := h.auth.ManageCredentials(r, change)
	if err != nil {
		status := http.StatusServiceUnavailable
		switch {
		case errors.Is(err, auth.ErrCredentialSession), errors.Is(err, auth.ErrCredentialManaged), errors.Is(err, auth.ErrCredentialPassword):
			status = http.StatusForbidden
		case errors.Is(err, auth.ErrCredentialInput), errors.Is(err, auth.ErrCredentialNoChange):
			status = http.StatusBadRequest
		case errors.Is(err, auth.ErrCredentialRate):
			status = http.StatusTooManyRequests
		case errors.Is(err, auth.ErrCredentialChanged):
			status = http.StatusConflict
		}
		fail(status, err.Error())
		return
	}
	if result.SignedOut {
		h.auth.ClearSessionCookie(w)
	}
	reply(http.StatusOK, result)
}
