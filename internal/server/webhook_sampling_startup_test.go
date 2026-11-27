package server

import (
	"context"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/engine"
	"sync"
	"testing"
	"time"
)

type startupObservationEngine struct {
	engine.Engine
	once   sync.Once
	called chan struct{}
}

func (e *startupObservationEngine) ListContainers(context.Context) ([]engine.ContainerInfo, error) {
	e.once.Do(func() { close(e.called) })
	return []engine.ContainerInfo{}, nil
}
func TestProductionWebhookStartupStartsAndJoinsRuntimeSampling(t *testing.T) {
	s := webhookServer(t)
	runtime := &startupObservationEngine{called: make(chan struct{})}
	runner := dill.New("must-not-execute")
	runner.Engine = "docker"
	runner.Socket = "/synthetic/runtime.sock"
	s.agent = agent.NewLocalAgent(runtime, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if e := s.prepareWebhooks(ctx); e != nil {
		t.Fatal(e)
	}
	defer s.webhooks.Close()
	select {
	case <-runtime.called:
	case <-time.After(3 * time.Second):
		t.Fatal("production startup did not start observations")
	}
	if e := s.webhooks.Close(); e != nil {
		t.Fatal(e)
	}
	if s.webhooks.Health().Observations != "stopped" {
		t.Fatal("sampler survived service close")
	}
}
