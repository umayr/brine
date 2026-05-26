package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/store"
)

// Mount registers all /api/* routes on r.
func Mount(r chi.Router, ag agent.Agent, st store.StackStore, rs store.RecipeStore, gr *git.Runner) {
	sh := &stackHandler{agent: ag, store: st, git: gr}
	ch := &containerHandler{agent: ag}
	ih := &imageHandler{agent: ag}
	rh := &recipeHandler{store: rs}

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", health)

		// Stacks
		r.Route("/stacks", func(r chi.Router) {
			r.Get("/", sh.list)
			r.Post("/", sh.create)
			r.Post("/git", sh.createFromGit)
			r.Route("/{name}", func(r chi.Router) {
				r.Get("/", sh.get)
				r.Put("/", sh.update)
				r.Delete("/", sh.delete)
				r.Post("/up", sh.up)
				r.Post("/down", sh.down)
				r.Post("/stop", sh.stop)
				r.Post("/start", sh.start)
				r.Post("/restart", sh.restart)
				r.Post("/teardown", sh.teardown)
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

				// Git operations
				r.Get("/git", sh.gitInfo)
				r.Patch("/git", sh.gitUpdateSource)
				r.Post("/git/pull", sh.gitPull)
				r.Post("/git/push", sh.gitPush)
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
				r.Get("/logs", ch.logs)
			})
		})

		// Images
		r.Route("/images", func(r chi.Router) {
			r.Get("/", ih.list)
			r.Post("/pull", ih.pull)
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
}
