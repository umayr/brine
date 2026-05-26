package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/store"
)

type recipeHandler struct {
	store store.RecipeStore
}

func (h *recipeHandler) list(w http.ResponseWriter, r *http.Request) {
	recipes, err := h.store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, recipes)
}

func (h *recipeHandler) get(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	recipe, err := h.store.Get(slug)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, recipe)
}

func (h *recipeHandler) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Category    string `json:"category"`
		Description string `json:"description"`
		Body        string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Name == "" || body.Body == "" {
		writeError(w, http.StatusBadRequest, "name and body are required")
		return
	}
	recipe, err := h.store.Create(body.Name, body.Category, body.Description, body.Body)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, recipe)
}

func (h *recipeHandler) update(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	recipe, err := h.store.Update(slug, body.Body)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, recipe)
}

func (h *recipeHandler) updateMeta(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	var body struct {
		Name        string `json:"name"`
		Category    string `json:"category"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	recipe, err := h.store.UpdateMeta(slug, body.Name, body.Category, body.Description)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, recipe)
}

func (h *recipeHandler) delete(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if err := h.store.Delete(slug); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
