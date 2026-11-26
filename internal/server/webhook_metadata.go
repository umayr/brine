package server

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/webhooks"
)

func (s *Server) configureWebhookMetadata(ctx context.Context, svc *webhooks.Service) error {
	observed, ok := s.store.(interface {
		SetMetadataObserver(store.MetadataObserver)
		ObserveMetadata(string) (*store.Stack, bool, error)
	})
	if !ok {
		return errors.New("webhook source observation requires an authoritative metadata store")
	}
	if e := svc.ConfigureMetadataObservation(func(ctx context.Context, name string) (webhooks.MetadataObservation, error) {
		if ctx.Err() != nil {
			return webhooks.MetadataObservation{}, ctx.Err()
		}
		st, exists, e := observed.ObserveMetadata(name)
		if e != nil {
			return webhooks.MetadataObservation{}, webhooks.ErrInvalid
		}
		if !exists {
			return webhooks.MetadataObservation{Exists: false}, nil
		}
		return webhooks.MetadataObservation{Exists: true, StackID: webhookStackID(st), Head: st.EventHead}, nil
	}); e != nil {
		return e
	}
	if e := svc.ReconcileMetadata(ctx); e != nil {
		return e
	}
	observed.SetMetadataObserver(func(change store.MetadataChange) (store.MetadataCommit, error) {
		st := change.After
		if st == nil {
			st = change.Before
		}
		if st == nil {
			return store.MetadataCommit{}, webhooks.ErrInvalid
		}
		sid := webhookStackID(st)
		if sid == "" {
			return store.MetadataCommit{}, webhooks.ErrInvalid
		}
		beforeHead := ""
		if change.Before != nil {
			beforeHead = change.Before.EventHead
		}
		source := "workspace"
		if st.Git != nil {
			source = "git"
		}
		base := webhooks.Event{Stack: webhooks.Resource{ID: sid, Name: st.Name, Kind: "stack"}, Source: webhooks.Resource{Kind: source}, Target: webhooks.Resource{Kind: "none"}, Outcome: "success"}
		event := base
		event.Type = "stack.updated"
		if change.Before == nil {
			event.Type = "stack.created"
		}
		if change.After == nil {
			event.Type = "stack.deleted"
		}
		events := []webhooks.Event{event}
		if change.After != nil && change.After.SourceHistory != nil {
			previous := map[string]bool{}
			if change.Before != nil && change.Before.SourceHistory != nil {
				for _, id := range change.Before.SourceHistory.Revisions {
					previous[id] = true
				}
			}
			for _, id := range change.After.SourceHistory.Revisions {
				if !previous[id] {
					event := base
					event.Type = "source.revision_created"
					event.Source.ID = id
					events = append(events, event)
				}
			}
			if change.Kind == "restore" {
				event := base
				event.Type = "source.restored"
				event.Action = "restore-source"
				event.Reason = "reviewed_source_only"
				revisions := change.After.SourceHistory.Revisions
				if len(revisions) > 0 {
					event.Source.ID = revisions[len(revisions)-1]
				}
				events = append(events, event)
			}
		}
		prepareCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		id, e := svc.BeginMetadataMutation(prepareCtx, st.Name, sid, beforeHead, change.Kind, events)
		if e != nil {
			return store.MetadataCommit{}, e
		}
		return store.MetadataCommit{ID: id, Done: func(commitErr error) {
			if commitErr != nil {
				return
			}
			saveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if e := svc.ConfirmMetadataMutation(saveCtx, id, sid); e != nil {
				log.Print("Webhook source metadata confirmation deferred; durable commit marker will be reconciled")
			}
		}}, nil
	})
	return nil
}
