package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/webhooks"
)

func reported(value string) webhooks.Observation {
	return webhooks.Observation{Availability: "reported", Value: &value}
}
func unavailable() webhooks.Observation { return webhooks.Observation{Availability: "unavailable"} }
func containerHealth(c *engine.ContainerInfo) webhooks.Observation {
	if c == nil {
		return unavailable()
	}
	if c.HealthStatus != nil {
		return reported(*c.HealthStatus)
	}
	if c.HealthConfigured != nil && !*c.HealthConfigured {
		return webhooks.Observation{Availability: "not_configured"}
	}
	return unavailable()
}

// Aggregate only fully observed membership. Unchecked containers do not prove
// health. Non-running, missing or unrecognized health values remain unavailable.
func aggregateHealth(containers []engine.ContainerInfo, health []webhooks.Observation) webhooks.Observation {
	if len(containers) == 0 {
		return webhooks.Observation{Availability: "empty"}
	}
	unchecked := 0
	starting, unhealthy := false, false
	for i, c := range containers {
		if c.State != "running" || i >= len(health) {
			return unavailable()
		}
		v := health[i]
		if v.Availability == "not_configured" {
			unchecked++
			continue
		}
		if v.Availability != "reported" || v.Value == nil {
			return unavailable()
		}
		switch *v.Value {
		case "unhealthy":
			unhealthy = true
		case "starting":
			starting = true
		case "healthy":
		default:
			return unavailable()
		}
	}
	if unchecked == len(containers) {
		return webhooks.Observation{Availability: "not_configured"}
	}
	if unchecked > 0 {
		return unavailable()
	}
	if unhealthy {
		return reported("unhealthy")
	}
	if starting {
		return reported("starting")
	}
	return reported("healthy")
}
func observationKey(e webhooks.Event) string {
	container := ""
	if e.Container != nil {
		container = e.Container.ID
	}
	sum := sha256.Sum256([]byte(e.Type + "\x00" + e.Target.ID + "\x00" + e.Stack.ID + "\x00" + container))
	return fmt.Sprintf("%x", sum)
}
func (s *Server) sampleLocalRuntime(ctx context.Context) error {
	if s.agent == nil || s.agent.Engine() == nil || s.agent.Dill() == nil {
		return webhooks.ErrInvalid
	}
	target := webhooks.Resource{ID: (&webhookDispatcher{s: s}).localTargetID(), Name: "Local", Kind: s.agent.Dill().Engine}
	seen := map[string]bool{}
	observe := func(e webhooks.Event, v webhooks.Observation) error {
		e.Target = target
		key := observationKey(e)
		seen[key] = true
		return s.webhooks.Observe(ctx, key, e, v)
	}
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	listed, e := s.agent.Engine().ListContainers(readCtx)
	connection := reported("available")
	if e != nil {
		connection = unavailable()
	}
	if err := observe(webhooks.Event{Type: "target.connectivity_changed"}, connection); err != nil {
		return err
	}
	if e != nil {
		if err := s.webhooks.MarkUnobserved(ctx, target.ID, nil); err != nil {
			return err
		}
		return webhooks.ErrInvalid
	}
	if len(listed) > 256 {
		if err := s.webhooks.MarkUnobserved(ctx, target.ID, nil); err != nil {
			return err
		}
		return webhooks.ErrCapacity
	}
	stacks, e := s.store.List()
	if e != nil {
		if err := s.webhooks.MarkUnobserved(ctx, target.ID, nil); err != nil {
			return err
		}
		return webhooks.ErrInvalid
	}
	blocked := map[string]bool{}
	if recovery, ok := s.store.(store.ManualDeploymentStore); ok {
		intents, err := recovery.ListManualIntents()
		if err != nil {
			if err = s.webhooks.MarkUnobserved(ctx, target.ID, nil); err != nil {
				return err
			}
			return webhooks.ErrInvalid
		}
		for _, i := range intents {
			if !i.Resolved {
				blocked[i.Stack] = true
			}
		}
	} else {
		if err := s.webhooks.MarkUnobserved(ctx, target.ID, nil); err != nil {
			return err
		}
		return webhooks.ErrInvalid
	}
	byName := map[string]*store.Stack{}
	for _, st := range stacks {
		if !blocked[st.Name] && (st.RemoteGit == nil || (!st.RemoteGit.Enabled && st.RemoteGit.Request == "")) {
			byName[st.Name] = st
		}
	}
	grouped := map[string][]engine.ContainerInfo{}
	healths := map[string][]webhooks.Observation{}
	for _, c := range listed {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		base := webhooks.Event{Container: &webhooks.Resource{ID: c.ID, Name: c.Name, Kind: "container"}}
		st := byName[c.Stack]
		if c.Labels["dill.managed"] != "true" || c.Stack == "" || c.Labels["dill.stack"] != c.Stack {
			st = nil
		}
		if st != nil {
			base.Stack = webhooks.Resource{ID: webhookStackID(st), Name: st.Name, Kind: "stack"}
			kind := "workspace"
			if st.Git != nil {
				kind = "git"
			}
			base.Source = webhooks.Resource{Kind: kind}
		}
		base.Type = "container.state_changed"
		if e = observe(base, reported(c.State)); e != nil {
			return e
		}
		inspected, err := s.agent.Engine().InspectContainer(readCtx, c.ID)
		h := unavailable()
		if err == nil && inspected != nil && inspected.ID == c.ID && inspected.State == c.State {
			h = containerHealth(inspected)
		}
		base.Type = "container.health_changed"
		if e = observe(base, h); e != nil {
			return e
		}
		if st != nil {
			grouped[st.Name] = append(grouped[st.Name], c)
			healths[st.Name] = append(healths[st.Name], h)
		}
	}
	for name, st := range byName {
		// A workspace alone does not attest a local execution target. Missing
		// prior local membership is invalidated below, never invented as empty.
		if len(grouped[name]) == 0 {
			continue
		}
		base := webhooks.Event{Type: "stack.health_changed", Stack: webhooks.Resource{ID: webhookStackID(st), Name: name, Kind: "stack"}}
		if e = observe(base, aggregateHealth(grouped[name], healths[name])); e != nil {
			return e
		}
	}
	return s.webhooks.MarkUnobserved(ctx, target.ID, seen)
}
