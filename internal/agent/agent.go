package agent

import (
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/engine"
)

// Agent is the central abstraction for the manager/agent architecture.
// Currently only LocalAgent is implemented; future remote agents will
// implement this interface and communicate over HTTP.
type Agent interface {
	// ID returns a unique identifier for this agent instance.
	ID() string

	// Name returns a human-readable label (e.g. "local", "prod-server").
	Name() string

	// Engine returns the container runtime interface for this agent.
	Engine() engine.Engine

	// Dill returns the dill runner for stack lifecycle operations.
	Dill() *dill.Runner
}

// LocalAgent is an Agent that runs in the same process as the brine server,
// talking directly to the local container runtime.
type LocalAgent struct {
	id     string
	name   string
	eng    engine.Engine
	runner *dill.Runner
}

// NewLocalAgent creates a LocalAgent with the given engine and dill runner.
func NewLocalAgent(eng engine.Engine, runner *dill.Runner) *LocalAgent {
	return &LocalAgent{
		id:     "local",
		name:   "local",
		eng:    eng,
		runner: runner,
	}
}

func (a *LocalAgent) ID() string            { return a.id }
func (a *LocalAgent) Name() string          { return a.name }
func (a *LocalAgent) Engine() engine.Engine { return a.eng }
func (a *LocalAgent) Dill() *dill.Runner    { return a.runner }
