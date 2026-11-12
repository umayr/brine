//go:build unix

package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/secretbindings"
	"github.com/umayr/brine/internal/store"
)

func (h *stackHandler) secretRunner(w http.ResponseWriter, r *http.Request) (*dill.SecretRunner, *store.Stack, string, bool) {
	if !auth.Authenticated(r) {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return nil, nil, "", false
	}
	if h.agent == nil || h.agent.Dill() == nil {
		writeError(w, http.StatusServiceUnavailable, "secret workflow unavailable")
		return nil, nil, "", false
	}
	strict, ok := h.agent.Dill().StrictActions.(*dill.StrictController)
	if !ok || strict.Secret == nil {
		writeError(w, http.StatusServiceUnavailable, "secret workflow unavailable: configure strict secret mode")
		return nil, nil, "", false
	}
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, "stack unavailable")
		return nil, nil, "", false
	}
	id := secretbindings.DeploymentID(name)
	if err = strict.Secret.Runner.CheckSecretCapabilities(r.Context(), strict.Secret.Engine); err != nil {
		if strict.Secret.State != nil {
			_ = strict.Secret.State.InvalidatePreview(id)
		}
		writeError(w, http.StatusServiceUnavailable, "secret workflow unavailable: install a compatible Dill candidate")
		return nil, nil, "", false
	}
	return strict.Secret, stack, id, true
}
func secretFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, secretbindings.ErrStale):
		writeError(w, http.StatusConflict, "Preview is stale; preview again.")
	case errors.Is(err, dill.ErrSecretApplyFailed):
		writeError(w, http.StatusBadGateway, "Activation or deployment failed; partial changes may have occurred. Check the active binding before retrying.")
	case errors.Is(err, dill.ErrSecretCapabilities):
		writeError(w, http.StatusServiceUnavailable, "secret workflow unavailable: install a compatible Dill candidate")
	default:
		writeError(w, http.StatusBadRequest, "Secret configuration or source unavailable.")
	}
}
func (h *stackHandler) secretBindingsGet(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	s, stack, id, ok := h.secretRunner(w, r)
	if !ok {
		return
	}
	draft, err := s.State.Draft(id, stack.Name, s.Target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		secretFailure(w, err)
		return
	}
	active, err := s.State.Active(id, stack.Name, s.Target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		secretFailure(w, err)
		return
	}
	var d, a json.RawMessage
	if draft != nil {
		d = draft
	}
	if active != nil {
		a = active
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"target": s.Target, "stack": stack.Name, "draft": d, "active": a})
}
func (h *stackHandler) secretBindingsPut(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	s, stack, id, ok := h.secretRunner(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, secretbindings.MaxDocument)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		secretFailure(w, err)
		return
	}
	if _, err = secretbindings.Parse(data, stack.Name, s.Target); err != nil {
		secretFailure(w, err)
		return
	}
	if err = s.State.SaveDraft(id, stack.Name, s.Target, data); err != nil {
		secretFailure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"status": "draft-saved"})
}
func (h *stackHandler) secretStatus(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	s, stack, id, ok := h.secretRunner(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.CheckLocked(ctx, id, stack.Name, stack.PklPath, r.Method != http.MethodGet); err != nil {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]string{"status": "unavailable"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"status": "available"})
}
func (h *stackHandler) secretPreview(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	s, stack, id, ok := h.secretRunner(w, r)
	if !ok {
		return
	}
	ctx, cancel := httpx.Detached(r.Context(), h.deployTimeout)
	defer cancel()
	plan, token, err := s.PreviewLocked(ctx, id, stack.Name, stack.PklPath)
	if err != nil {
		secretFailure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"plan": plan, "preview_token": token})
}
func readPreviewToken(r *http.Request) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1025))
	if err != nil || len(b) > 1024 {
		return "", secretbindings.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') || !d.More() {
		return "", secretbindings.ErrInvalid
	}
	key, err := d.Token()
	if err != nil || key != "preview_token" {
		return "", secretbindings.ErrInvalid
	}
	var raw json.RawMessage
	if d.Decode(&raw) != nil || len(raw) == 0 || raw[0] != '"' || d.More() {
		return "", secretbindings.ErrInvalid
	}
	tok, err = d.Token()
	if err != nil || tok != json.Delim('}') {
		return "", secretbindings.ErrInvalid
	}
	if _, err = d.Token(); err != io.EOF {
		return "", secretbindings.ErrInvalid
	}
	var token string
	if json.Unmarshal(raw, &token) != nil {
		return "", secretbindings.ErrInvalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != token {
		return "", secretbindings.ErrInvalid
	}
	return token, nil
}
func (h *stackHandler) secretConfirm(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	token, err := readPreviewToken(r)
	if err != nil {
		secretFailure(w, err)
		return
	}
	s, stack, id, ok := h.secretRunner(w, r)
	if !ok {
		return
	}
	ctx, cancel := httpx.Detached(r.Context(), h.deployTimeout)
	defer cancel()
	if err = s.ConfirmAndApplyLocked(ctx, id, stack.Name, stack.PklPath, token); err != nil {
		secretFailure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"status": "applied"})
}
