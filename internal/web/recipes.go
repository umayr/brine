package web

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (h *Handler) recipeList(w http.ResponseWriter, r *http.Request) {
	byCategory, err := h.recipes.ListByCategory()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.render(w, r, "recipes/index.html", map[string]interface{}{
		"Page":       "recipes",
		"ByCategory": byCategory,
	})
}

func (h *Handler) recipeNew(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "recipes/editor.html", map[string]interface{}{
		"Page":   "recipes",
		"Recipe": nil,
	})
}

func (h *Handler) recipeEditor(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	recipe, err := h.recipes.Get(slug)
	if err != nil {
		http.Error(w, "Recipe not found", http.StatusNotFound)
		return
	}
	h.render(w, r, "recipes/editor.html", map[string]interface{}{
		"Page":   "recipes",
		"Recipe": recipe,
	})
}

// --- HTMX partials ---

func (h *Handler) recipeCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderAlert(w, "error", "Invalid form data")
		return
	}
	name := r.FormValue("name")
	category := r.FormValue("category")
	description := r.FormValue("description")
	body := r.FormValue("body")

	if name == "" || body == "" {
		h.renderAlert(w, "error", "Name and body are required")
		return
	}
	recipe, err := h.recipes.Create(name, category, description, body)
	if err != nil {
		h.renderAlert(w, "error", err.Error())
		return
	}
	w.Header().Set("HX-Redirect", "/recipes/"+recipe.Slug+"/edit")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) recipeUpdate(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if err := r.ParseForm(); err != nil {
		h.renderAlert(w, "error", "Invalid form data")
		return
	}
	body := r.FormValue("body")
	name := r.FormValue("name")
	category := r.FormValue("category")
	description := r.FormValue("description")

	if _, err := h.recipes.Update(slug, body); err != nil {
		h.renderAlert(w, "error", err.Error())
		return
	}
	if name != "" {
		if _, err := h.recipes.UpdateMeta(slug, name, category, description); err != nil {
			h.renderAlert(w, "error", err.Error())
			return
		}
	}
	h.renderAlert(w, "success", "Saved")
}

func (h *Handler) recipeDelete(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if err := h.recipes.Delete(slug); err != nil {
		h.renderAlert(w, "error", err.Error())
		return
	}
	w.Header().Set("HX-Redirect", "/recipes")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) recipeRows(w http.ResponseWriter, r *http.Request) {
	byCategory, err := h.recipes.ListByCategory()
	if err != nil {
		h.renderAlert(w, "error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	for cat, recipes := range byCategory {
		fmt.Fprintf(w, `<tr class="bg-base-200"><td colspan="4" class="py-2 px-4 text-xs font-semibold text-base-content/60 uppercase tracking-wider">%s</td></tr>`, cat)
		for _, recipe := range recipes {
			fmt.Fprintf(w, `<tr class="hover" id="recipe-%s">
  <td class="font-mono text-sm">%s</td>
  <td class="text-xs text-base-content/60">%s</td>
  <td class="text-sm">%s</td>
  <td class="text-right">
    <a href="/recipes/%s/edit" class="btn btn-ghost btn-xs">Edit</a>
    <button class="btn btn-ghost btn-xs text-error"
      hx-delete="/web/recipes/%s"
      hx-target="#recipe-%s"
      hx-swap="outerHTML"
      hx-confirm="Delete recipe %q?"
    >Delete</button>
  </td>
</tr>`, recipe.Slug, recipe.Name, recipe.Category, recipe.Description, recipe.Slug, recipe.Slug, recipe.Slug, recipe.Name)
		}
	}
}
