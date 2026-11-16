package web

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/auth"
)

var errVariableSession = errors.New("session unavailable")

type variablesData struct {
	pageData
	Values   map[string]string
	Error    string
	Disabled bool
}

type stackVariableRow struct {
	Name, Value, SharedValue string
	Local, HasShared         bool
}

type stackVariablesData struct {
	Rows        []stackVariableRow
	Unavailable bool
	Disabled    bool
	Error       string
}

func (h *Handler) variableModeDisabled() bool {
	if h.agent == nil || h.agent.Dill() == nil {
		return true
	}
	r := h.agent.Dill()
	return r.Profile == "alpha" || r.StrictActions != nil || r.QualifiedOperation != nil
}

func (h *Handler) stackVariables(name, message string) stackVariablesData {
	data := stackVariablesData{Error: message, Disabled: h.variableModeDisabled()}
	if h.agent == nil || h.agent.Dill() == nil || h.agent.Dill().Variables == nil {
		data.Unavailable = true
		return data
	}
	shared, local, err := h.agent.Dill().Variables.Snapshot(name)
	if err != nil {
		data.Unavailable = true
		return data
	}
	names := make(map[string]struct{}, len(shared)+len(local))
	for name := range shared {
		names[name] = struct{}{}
	}
	for name := range local {
		names[name] = struct{}{}
	}
	for name := range names {
		sharedValue, hasShared := shared[name]
		value, localValue := local[name]
		if !localValue {
			value = sharedValue
		}
		data.Rows = append(data.Rows, stackVariableRow{Name: name, Value: value, SharedValue: sharedValue, Local: localValue, HasShared: hasShared})
	}
	sort.Slice(data.Rows, func(i, j int) bool {
		if data.Rows[i].Local != data.Rows[j].Local {
			return !data.Rows[i].Local
		}
		return data.Rows[i].Name < data.Rows[j].Name
	})
	return data
}

func (h *Handler) variablesPage(w http.ResponseWriter, r *http.Request) {
	if name := chi.URLParam(r, "name"); name != "" {
		if _, err := h.store.Get(name); err != nil {
			http.Error(w, "Stack not found", http.StatusNotFound)
			return
		}
		http.Redirect(w, r, "/stacks/"+name+"#variables", http.StatusSeeOther)
		return
	}
	if r.URL.Path == "/settings/variables" {
		http.Redirect(w, r, "/variables", http.StatusMovedPermanently)
		return
	}
	h.variablesRender(w, r, "")
}

func (h *Handler) variablesRender(w http.ResponseWriter, r *http.Request, message string) {
	if h.agent == nil || h.agent.Dill() == nil || h.agent.Dill().Variables == nil {
		http.Error(w, "Configuration variables unavailable", http.StatusServiceUnavailable)
		return
	}
	values, err := h.agent.Dill().Variables.Values("")
	if err != nil {
		http.Error(w, "Configuration variables unavailable", http.StatusServiceUnavailable)
		return
	}
	h.render(w, r, "settings/variables.html", &variablesData{pageData: pageData{Page: "variables"}, Values: values, Error: message, Disabled: h.variableModeDisabled()})
}

func (h *Handler) variablesSave(w http.ResponseWriter, r *http.Request) {
	if h.agent == nil || h.agent.Dill() == nil || h.agent.Dill().Variables == nil || h.variableModeDisabled() {
		http.Error(w, "Configuration variables unavailable in this profile", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 20000)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	stack := chi.URLParam(r, "name")
	if stack != "" {
		if _, err := h.store.Get(stack); err != nil {
			http.Error(w, "Stack not found", http.StatusNotFound)
			return
		}
	}
	name := strings.TrimSpace(r.PostForm.Get("name"))
	action := r.PostForm.Get("action")
	if action != "save" && action != "delete" {
		http.Error(w, "Invalid action", http.StatusBadRequest)
		return
	}
	var authority func(context.Context) (context.Context, func(), error)
	if auth.CookieAuthenticated(r) {
		authority = func(context.Context) (context.Context, func(), error) {
			if h.auth == nil {
				return nil, nil, errVariableSession
			}
			ctx, release, ok := h.auth.SubmissionLease(r)
			if !ok {
				return nil, nil, errVariableSession
			}
			return ctx, release, nil
		}
	}
	if err := h.agent.Dill().Variables.SetContext(r.Context(), stack, name, r.PostForm.Get("value"), action == "delete", authority); err != nil {
		if r.Context().Err() != nil {
			http.Error(w, "Save cancelled", http.StatusRequestTimeout)
			return
		}
		if auth.CookieAuthenticated(r) && errors.Is(err, errVariableSession) {
			http.Error(w, "Session unavailable", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		if stack != "" {
			h.stackDetailWithVariableError(w, r, err.Error())
		} else {
			h.variablesRender(w, r, err.Error())
		}
		return
	}
	target := "/variables"
	if stack != "" {
		target = "/stacks/" + stack + "#variables"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
