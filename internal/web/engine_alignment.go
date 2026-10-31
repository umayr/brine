package web

import (
	"github.com/umayr/brine/internal/pkl"
	"github.com/umayr/brine/internal/store"
)

type engineAlignment struct {
	RuntimeEngine  string
	DeclaredEngine string
	Warning        string
	Blocked        bool
}

func (a engineAlignment) HasWarning() bool {
	return a.Warning != ""
}

func (h *Handler) engineAlignmentFor(stack *store.Stack, files map[string]string) engineAlignment {
	alignment := engineAlignment{
		RuntimeEngine: h.engine,
	}
	if alignment.RuntimeEngine == "" {
		alignment.RuntimeEngine = "docker"
	}

	entrypoint := stack.Entrypoint
	if entrypoint == "" {
		entrypoint = "compose.pkl"
	}
	alignment.DeclaredEngine = pkl.DeclaredEngine(files[entrypoint])

	if alignment.DeclaredEngine != "" && alignment.DeclaredEngine != alignment.RuntimeEngine {
		alignment.Warning = "This Pkl declares " + alignment.DeclaredEngine + ", but Brine is connected to " + alignment.RuntimeEngine + "."
		alignment.Blocked = true
	}

	return alignment
}
