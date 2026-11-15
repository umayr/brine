//go:build !linux

package main

import (
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/web"
)

func prepareSyntheticRunner(*dill.Runner) error {
	return errors.New("synthetic attended fixture requires Linux; no qualification implied")
}
func mountSyntheticAttended(chi.Router, agent.Agent, *dill.Runner, store.StackStore, store.RecipeStore, *git.Runner, *web.Handler, httpx.Timeouts, string) (func(), error) {
	return nil, errors.New("synthetic attended fixture requires Linux; no qualification implied")
}
