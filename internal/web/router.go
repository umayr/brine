package web

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
)

//go:embed static
var staticFS embed.FS

// Mount registers all web routes on r.
func Mount(r chi.Router, h *Handler) {
	// Static assets
	staticSub, _ := fs.Sub(staticFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// Full-page routes
	r.Get("/", h.dashboard)
	r.Get("/stacks", h.stackList)
	r.Get("/stacks/new", h.stackNew)
	r.Get("/stacks/{name}", h.stackDetail)
	r.Get("/stacks/{name}/edit", h.stackEditor)
	r.Get("/containers", h.containerList)
	r.Get("/containers/{id}", h.containerDetail)
	r.Get("/images", h.imageList)
	r.Get("/recipes", h.recipeList)
	r.Get("/recipes/new", h.recipeNew)
	r.Get("/recipes/{slug}/edit", h.recipeEditor)

	// HTMX partial routes
	r.Route("/web", func(r chi.Router) {
		// Stack partials
		r.Post("/stacks", h.stackCreate)
		r.Post("/stacks/git", h.stackGitCreate)
		r.Put("/stacks/{name}", h.stackUpdate)
		r.Post("/stacks/{name}/validate", h.stackValidate)
		r.Post("/stacks/{name}/fmt", h.stackFmt)
		r.Get("/stacks/{name}/plan", h.stackPlan)
		r.Get("/stacks/{name}/containers", h.stackContainers)

		// Stack file partials
		r.Post("/stacks/{name}/files", h.stackFileCreate)
		r.Put("/stacks/{name}/files/{filename}", h.stackFileUpdate)
		r.Delete("/stacks/{name}/files/{filename}", h.stackFileDelete)
		r.Post("/stacks/{name}/entrypoint", h.stackSetEntrypoint)

		// Git partials
		r.Post("/stacks/{name}/git/pull", h.stackGitPull)
		r.Put("/stacks/{name}/git", h.stackGitUpdateSource)

		// Container partials
		r.Get("/containers", h.containerRows)
		r.Post("/containers/{id}/start", h.containerStart)
		r.Post("/containers/{id}/stop", h.containerStop)
		r.Post("/containers/{id}/restart", h.containerRestart)
		r.Get("/containers/{id}/logs", h.containerLogsSSE)

		// Image partials
		r.Get("/images", h.imageRows)
		r.Post("/images/pull", h.imagePull)

		// Recipe partials
		r.Post("/recipes", h.recipeCreate)
		r.Put("/recipes/{slug}", h.recipeUpdate)
		r.Delete("/recipes/{slug}", h.recipeDelete)
		r.Get("/recipes", h.recipeRows)
	})
}
