package web

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/httpx"
)

//go:embed static
var staticFS embed.FS

// Mount registers all web routes on r.
func Mount(r chi.Router, h *Handler, timeouts httpx.Timeouts) {
	h.mountWebhooks(r)
	// Static assets use the same source tree as templates in dev mode.
	var source fs.FS = staticFS
	if h.dev {
		source = h.devFS
	}
	var staticHandler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "static filesystem unavailable", http.StatusInternalServerError)
	})
	if source != nil {
		if staticSub, err := fs.Sub(source, "static"); err == nil {
			staticHandler = http.StripPrefix("/static/", http.FileServer(http.FS(staticSub)))
		}
	}
	// Require revalidation so a long-running browser session sees updated assets
	// after a rebuild or upgrade. This applies to embedded and dev files alike.
	r.Handle("/static/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		staticHandler.ServeHTTP(w, r)
	}))

	// Log streaming must not be cut by a request timeout.
	r.Get("/web/containers/{id}/logs", h.containerLogsSSE)

	// Git network operations get the lifecycle timeout.
	r.Group(func(r chi.Router) {
		r.Use(httpx.Timeout(timeouts.Lifecycle))
		r.Post("/web/stacks/{name}/git/pull", h.stackGitPull)
		r.Post("/web/stacks/{name}/broker/prepare", h.brokerPrepare)
		r.Post("/web/stacks/{name}/broker/confirm", h.brokerConfirmPlan)
		r.Post("/web/stacks/{name}/broker/status", h.brokerReconcile)
	})

	// Git creation supplies its own bounded request-cancellable deadline.
	r.Post("/web/stacks/git", h.stackGitCreate)

	// Everything else is short-lived.
	r.Group(func(r chi.Router) {
		r.Use(httpx.Timeout(timeouts.Default))

		// Full-page routes
		r.Get("/", h.dashboard)
		r.Get("/settings", h.settingsAppearance)
		r.Get("/settings/secrets", h.brokerSettings)
		r.Post("/stacks/{name}/secrets", h.saveSecretBinding)
		r.Get("/settings/general", h.settingsGeneral)
		r.Post("/settings/general", h.settingsCredentials)
		r.Get("/settings/live-updates", h.settingsLiveUpdates)
		r.Get("/settings/source-history", h.settingsSourceHistory)
		r.Post("/settings/source-history", h.saveSourceHistorySettings)
		r.Get("/variables", h.variablesPage)
		r.Post("/variables", h.variablesSave)
		// Keep previously shared bookmarks and form submissions working.
		r.Get("/settings/variables", h.variablesPage)
		r.Post("/settings/variables", h.variablesSave)
		r.Get("/stacks/{name}/variables", h.variablesPage)
		r.Post("/stacks/{name}/variables", h.variablesSave)
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
			r.Get("/stacks", h.stackInventory)
			r.Post("/stacks", h.stackCreate)
			r.Put("/stacks/{name}", h.stackUpdate)
			r.Post("/stacks/{name}/validate", h.stackValidate)
			r.Post("/stacks/{name}/fmt", h.stackFmt)
			r.Get("/stacks/{name}/project-preview", h.projectPreview)
			r.Post("/stacks/{name}/staging/prepare", h.stagingPrepare)
			r.Post("/stacks/{name}/staging/confirm", h.stagingConfirm)
			r.Get("/stacks/{name}/history", h.stackHistory)
			r.Post("/stacks/{name}/history/review", h.stackSourceReview)
			r.Post("/stacks/{name}/history/confirm", h.stackSourceConfirm)
			r.Post("/stacks/{name}/history/auto-checkpoint", h.stackAutoCheckpoint)
			r.Get("/stacks/{name}/plan", h.stackPlan)
			r.Get("/stacks/{name}/containers", h.stackContainers)

			// Stack file partials
			r.Post("/stacks/{name}/files", h.stackFileCreate)
			r.Put("/stacks/{name}/files/{filename}", h.stackFileUpdate)
			r.Delete("/stacks/{name}/files/{filename}", h.stackFileDelete)
			r.Post("/stacks/{name}/entrypoint", h.stackSetEntrypoint)

			// Git partials (pull is mounted in the lifecycle group)
			r.Put("/stacks/{name}/git", h.stackGitUpdateSource)

			// Container partials
			r.Get("/containers", h.containerRows)
			r.Post("/containers/{id}/start", h.containerStart)
			r.Post("/containers/{id}/stop", h.containerStop)
			r.Post("/containers/{id}/restart", h.containerRestart)

			// Image partials
			r.Get("/images", h.imageRows)
			r.Post("/images/pull", h.imagePull)

			// Recipe partials
			r.Post("/recipes", h.recipeCreate)
			r.Put("/recipes/{slug}", h.recipeUpdate)
			r.Delete("/recipes/{slug}", h.recipeDelete)
			r.Get("/recipes", h.recipeRows)
		})
	})
}
