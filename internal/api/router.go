package api

import (
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
)

// Mount registers all /api/* routes on r.
func Mount(r chi.Router, ag agent.Agent, st store.StackStore, rs store.RecipeStore, gr *git.Runner, runtimeEngine string, timeouts httpx.Timeouts, deployTimeout time.Duration) {
	sh := &stackHandler{agent: ag, store: st, git: gr, runtimeEngine: runtimeEngine, deployTimeout: deployTimeout}
	ch := &containerHandler{agent: ag}
	ih := &imageHandler{agent: ag}
	rh := &recipeHandler{store: rs}

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", health)

		// Streaming endpoints must outlive the ordinary request timeout.
		r.Get("/containers/{id}/logs", ch.logs)
		r.Post("/images/pull", ih.pull)

		// Long-running lifecycle operations. These are bounded by the detached
		// command timeout, not by the HTTP request timeout.
		r.Group(func(r chi.Router) {
			r.Use(httpx.Timeout(timeouts.Lifecycle))
			r.Post("/stacks/{name}/up", sh.up)
			r.Post("/stacks/{name}/down", sh.down)
			r.Post("/stacks/{name}/stop", sh.stop)
			r.Post("/stacks/{name}/start", sh.start)
			r.Post("/stacks/{name}/restart", sh.restart)
			r.Post("/stacks/{name}/teardown", sh.teardown)
			r.Post("/stacks/{name}/git/pull", sh.gitPull)
			r.Post("/stacks/{name}/git/push", sh.gitPush)
		})

		// Everything else is short-lived.
		r.Group(func(r chi.Router) {
			r.Use(httpx.Timeout(timeouts.Default))

			// Stacks
			r.Route("/stacks", func(r chi.Router) {
				r.Get("/", sh.list)
				r.Post("/", sh.create)
				r.Post("/git", sh.createFromGit)
				r.Route("/{name}", func(r chi.Router) {
					r.Get("/", sh.get)
					r.Put("/", sh.update)
					r.Delete("/", sh.delete)
					r.Get("/plan", sh.plan)
					r.Get("/validate", sh.validate)
					r.Get("/containers", sh.containers)

					// Multi-file management
					r.Get("/files", sh.listFiles)
					r.Post("/entrypoint", sh.setEntrypoint)
					r.Route("/files/{filename}", func(r chi.Router) {
						r.Get("/", sh.getFile)
						r.Put("/", sh.writeFile)
						r.Delete("/", sh.deleteFile)
					})

					// Git operations (pull/push are mounted in the lifecycle group)
					r.Get("/git", sh.gitInfo)
					r.Patch("/git", sh.gitUpdateSource)
				})
			})

			// Containers
			r.Route("/containers", func(r chi.Router) {
				r.Get("/", ch.list)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", ch.get)
					r.Post("/start", ch.start)
					r.Post("/stop", ch.stop)
					r.Post("/restart", ch.restart)
				})
			})

			// Images
			r.Route("/images", func(r chi.Router) {
				r.Get("/", ih.list)
				r.Delete("/{id}", ih.remove)
			})

			// Recipes
			r.Route("/recipes", func(r chi.Router) {
				r.Get("/", rh.list)
				r.Post("/", rh.create)
				r.Route("/{slug}", func(r chi.Router) {
					r.Get("/", rh.get)
					r.Put("/", rh.update)
					r.Patch("/", rh.updateMeta)
					r.Delete("/", rh.delete)
				})
			})
		})
	})
}
