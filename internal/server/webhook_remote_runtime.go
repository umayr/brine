package server

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/remoteui"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/webhooks"
)

func (s *Server) sampleRuntime(ctx context.Context) error {
	var errs []error
	if s.agent != nil && s.agent.Engine() != nil && (&webhookDispatcher{s: s}).localTargetID() != "" {
		errs = append(errs, s.sampleLocalRuntime(ctx))
	}
	if s.remoteReadLeases != nil {
		errs = append(errs, s.sampleRemoteRuntime(ctx))
	}
	return errors.Join(errs...)
}
func (s *Server) sampleRemoteRuntime(ctx context.Context) error {
	if s.targetRegistry == nil || s.remoteReadLeases == nil || s.targetJournalScope == "" {
		return webhooks.ErrInvalid
	}
	rows, e := s.targetRegistry.ListContext(ctx)
	if e != nil {
		return e
	}
	if len(rows) == 0 {
		return nil
	}
	var errs []error
	start := s.remoteSampleOffset % len(rows)
	limit := len(rows)
	if limit > 2 {
		limit = 2
	}
	for n := 0; n < limit; n++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		index := (start + n) % len(rows)
		s.remoteSampleOffset = (index + 1) % len(rows)
		row := rows[index]
		target := webhooks.Resource{ID: webhookRegisteredTargetID(s.targetJournalScope, row.ID), Name: row.ID, Kind: "registered-target"}
		read, cancel := context.WithTimeout(ctx, 3*time.Second)
		lease, session, release, err := s.remoteReadLeases.Acquire(read, row.ID)
		var inventory []agentclient.InventoryItem
		health := map[string]webhooks.Observation{}
		caps := unavailable()
		binding := ""
		if err == nil && (lease == nil || session == nil || release == nil) {
			err = webhooks.ErrInvalid
		}
		if err == nil {
			if observed, ok := session.(remoteui.ObservationSession); ok {
				binding = observed.ObservationBinding()
				reportedCaps, capErr := observed.Capabilities(lease)
				if capErr == nil {
					sort.Strings(reportedCaps)
					body, _ := json.Marshal(reportedCaps)
					if len(body) <= 1024 {
						caps = reported(string(body))
					}
				}
			}
			inventory, err = session.Inventory(lease, 3)
			if len(inventory) > 256 {
				err = webhooks.ErrCapacity
			}
			if err == nil {
				for _, item := range inventory {
					h := unavailable()
					inspected, inspectErr := session.Inspect(lease, item.ID)
					if inspectErr == nil && inspected.ContainerID == item.ID && inspected.Stack == item.Stack && inspected.Service == item.Service && inspected.Inspect.State == item.State {
						if inspected.Inspect.Health.Status != nil {
							h = reported(*inspected.Inspect.Health.Status)
						} else if inspected.Inspect.Health.Test != nil && (len(inspected.Inspect.Health.Test) == 0 || inspected.Inspect.Health.Test[0] == "NONE") {
							h = webhooks.Observation{Availability: "not_configured"}
						}
					}
					health[item.ID] = h
				}
			}
			if lease.Err() != nil {
				err = lease.Err()
			}
		}
		if release != nil {
			err = errors.Join(err, release())
		}
		cancel()
		seen := map[string]bool{}
		observe := func(base webhooks.Event, value webhooks.Observation) error {
			base.Target = target
			key := observationKey(base)
			seen[key] = true
			return s.webhooks.Observe(ctx, key, base, value)
		}
		connection := reported("available")
		if err != nil {
			connection = unavailable()
			caps = unavailable()
		}
		if e = observe(webhooks.Event{Type: "target.connectivity_changed"}, connection); e != nil {
			return e
		}
		if e = observe(webhooks.Event{Type: "target.capabilities_changed"}, caps); e != nil {
			return e
		}
		if err != nil {
			errs = append(errs, err)
			if e = s.webhooks.MarkUnobserved(ctx, target.ID, nil); e != nil {
				return e
			}
			continue
		}
		stacks, storeErr := s.store.List()
		owners := map[string]*store.Stack{}
		if storeErr == nil && binding != "" {
			for _, st := range stacks {
				if st.RemoteGit != nil && st.RemoteGit.Host == row.ID && st.RemoteGit.Target == binding && (st.RemoteGit.Enabled || st.RemoteGit.Request != "") {
					owners[st.Name] = st
				}
			}
		}
		grouped := map[string][]engine.ContainerInfo{}
		groupHealth := map[string][]webhooks.Observation{}
		resources := map[string]webhooks.Resource{}
		for _, item := range inventory {
			stack := webhooks.Resource{ID: hookDigest("runtime_stack_", target.ID+"\x00"+item.Stack), Name: item.Stack, Kind: "runtime-stack"}
			source := webhooks.Resource{}
			if st := owners[item.Stack]; st != nil {
				stack = webhooks.Resource{ID: webhookStackID(st), Name: st.Name, Kind: "stack"}
				source = webhooks.Resource{Kind: "git"}
			}
			base := webhooks.Event{Stack: stack, Source: source, Container: &webhooks.Resource{ID: item.ID, Name: item.Name, Kind: "container"}, Type: "container.state_changed"}
			if e = observe(base, reported(item.State)); e != nil {
				return e
			}
			base.Type = "container.health_changed"
			if e = observe(base, health[item.ID]); e != nil {
				return e
			}
			grouped[stack.ID] = append(grouped[stack.ID], engine.ContainerInfo{State: item.State})
			groupHealth[stack.ID] = append(groupHealth[stack.ID], health[item.ID])
			resources[stack.ID] = stack
		}
		for id, members := range grouped {
			if e = observe(webhooks.Event{Type: "stack.health_changed", Stack: resources[id]}, aggregateHealth(members, groupHealth[id])); e != nil {
				return e
			}
		}
		if e = s.webhooks.MarkUnobserved(ctx, target.ID, seen); e != nil {
			return e
		}
		if storeErr != nil {
			errs = append(errs, storeErr)
		}
	}
	return errors.Join(errs...)
}
