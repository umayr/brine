package web

import (
	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/brokerapp"
	"github.com/umayr/brine/internal/secretbroker"
	"net/http"
	"strconv"
)

type brokerBindingScope struct {
	Scope       secretbroker.Scope
	Revision    int64
	Bindings    []secretbroker.Binding
	Connections []brokerapp.Connection
}
type brokerBindingsData struct {
	Available   bool
	Unavailable bool
	Scopes      []brokerBindingScope
}
type brokerSettingsData struct {
	pageData
	Available   bool
	Connections []brokerapp.Connection
}

func (h *Handler) brokerSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	d := brokerSettingsData{pageData: pageData{Page: "settings"}, Available: h.broker.Enabled()}
	if d.Available {
		d.Connections = h.broker.Connections()
	}
	h.render(w, r, "settings/secrets.html", &d)
}
func (h *Handler) stackSecretBindings(stack string) brokerBindingsData {
	d := brokerBindingsData{Available: h.broker.Enabled()}
	if !d.Available {
		d.Unavailable = h.broker != nil
		return d
	}
	for _, scope := range h.broker.Scopes(stack) {
		snapshot, err := h.broker.State.Load(scope)
		if err != nil {
			d.Unavailable = true
			return d
		}
		d.Scopes = append(d.Scopes, brokerBindingScope{Scope: scope, Revision: snapshot.Revision, Bindings: snapshot.Bindings, Connections: h.broker.ConnectionsFor(scope)})
	}
	return d
}
func (h *Handler) saveSecretBinding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.broker.Enabled() || h.auth == nil || !auth.CookieAuthenticated(r) {
		http.Error(w, "Browser authentication and configured secrets required", 403)
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
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.URL.RawQuery != "" || r.ParseForm() != nil {
		http.Error(w, "Invalid reference", 400)
		return
	}
	allowed := map[string]bool{"_csrf": true, "registration": true, "revision": true, "action": true, "name": true, "connection": true, "vaultPath": true, "property": true, "version": true, "folder": true, "key": true, "alias": true}
	for k, v := range r.PostForm {
		if !allowed[k] || len(v) != 1 {
			http.Error(w, "Invalid reference fields", 400)
			return
		}
	}
	var scope secretbroker.Scope
	for _, s := range h.broker.Scopes(name) {
		if s.RegistrationID == r.PostForm.Get("registration") {
			scope = s
		}
	}
	if scope.RegistrationID == "" {
		http.Error(w, "Binding scope unavailable", 403)
		return
	}
	revision, err := strconv.ParseInt(r.PostForm.Get("revision"), 10, 64)
	if err != nil || revision < 0 {
		http.Error(w, "Invalid revision", 400)
		return
	}
	snapshot, err := h.broker.State.Load(scope)
	if err != nil || snapshot.Revision != revision {
		http.Error(w, "Bindings changed or unavailable; reload before saving", 409)
		return
	}
	action, logical := r.PostForm.Get("action"), r.PostForm.Get("name")
	if action != "save" && action != "delete" {
		http.Error(w, "Invalid action", 400)
		return
	}
	next := []secretbroker.Binding{}
	for _, b := range snapshot.Bindings {
		if b.Name != logical {
			next = append(next, b)
		}
	}
	if action == "save" {
		binding := secretbroker.Binding{Name: logical, ConnectionID: r.PostForm.Get("connection")}
		kind, ok := h.broker.ConnectionKind(binding.ConnectionID)
		if !ok {
			http.Error(w, "Connection unavailable", 400)
			return
		}
		version := int64(0)
		if v := r.PostForm.Get("version"); v != "" {
			version, err = strconv.ParseInt(v, 10, 64)
			if err != nil || version < 0 {
				http.Error(w, "Invalid provider version", 400)
				return
			}
		}
		binding.Kind = kind
		switch kind {
		case secretbroker.ProviderVault:
			binding.Vault = secretbroker.VaultRef{Path: r.PostForm.Get("vaultPath"), Property: r.PostForm.Get("property"), Version: version}
		case secretbroker.ProviderInfisical:
			binding.Infisical = secretbroker.InfisicalRef{Folder: r.PostForm.Get("folder"), Key: r.PostForm.Get("key"), Version: version}
		case secretbroker.ProviderLocal:
			binding.LocalAlias = r.PostForm.Get("alias")
			if version != 0 {
				http.Error(w, "Local files do not support version pins", 400)
				return
			}
		}
		next = append(next, binding)
	} else {
		found := false
		for _, b := range snapshot.Bindings {
			if b.Name == logical {
				found = true
			}
		}
		if !found {
			http.Error(w, "Reference no longer exists", 409)
			return
		}
	}
	ctx, release, ok := h.auth.SubmissionLease(r)
	if !ok {
		http.Error(w, "Session unavailable", 403)
		return
	}
	defer release()
	if ctx.Err() != nil {
		http.Error(w, "Session unavailable", 403)
		return
	}
	if _, err = h.broker.Save(scope, revision, "browser", next); err != nil {
		http.Error(w, "Reference not saved; reload and review configuration", 409)
		return
	}
	http.Redirect(w, r, "/stacks/"+name+"#variables", http.StatusSeeOther)
}
