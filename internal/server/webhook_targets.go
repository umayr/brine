package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"time"

	"github.com/umayr/brine/internal/targetregistry"
	"github.com/umayr/brine/internal/webhooks"
)

// This identifies the configured logical record, not an independently attested
// daemon or an incoming execution grant. Credentials and transport paths stay private.
func webhookRegisteredTargetID(scope, id string) string {
	hash := sha256.Sum256([]byte("brine/registered-target/v1\x00" + scope + "\x00" + id))
	return hex.EncodeToString(hash[:])
}
func (s *Server) configureWebhookTargets(ctx context.Context, svc *webhooks.Service) error {
	if s.targetRegistry == nil {
		return nil
	}
	identity, e := s.targetRegistry.JournalIdentity(ctx)
	if e != nil {
		return e
	}
	scope := webhooks.TargetRegistryScope + identity
	s.targetJournalScope = scope
	s.targetRegistry.SetCommitObserver(func(ctx context.Context, before string, changes []targetregistry.CommitChange) (targetregistry.CommitReceipt, error) {
		events := make([]webhooks.Event, 0, len(changes))
		for _, c := range changes {
			row := c.After
			kind := "target.updated"
			if row == nil {
				row = c.Before
				kind = "target.removed"
			} else if c.Before == nil {
				kind = "target.enrolled"
			}
			if row == nil {
				return targetregistry.CommitReceipt{}, webhooks.ErrInvalid
			}
			events = append(events, webhooks.Event{Type: kind, Target: webhooks.Resource{ID: webhookRegisteredTargetID(scope, row.ID), Name: row.ID, Kind: "registered-target"}, Outcome: "success", Reason: "registration_committed"})
		}
		prepareCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		id, e := svc.BeginMetadataMutation(prepareCtx, scope, scope, before, "registry", events)
		if e != nil {
			return targetregistry.CommitReceipt{}, e
		}
		return targetregistry.CommitReceipt{ID: id, Done: func(commitErr error) {
			if commitErr != nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if e := svc.ConfirmMetadataMutation(ctx, id, scope); e != nil {
				log.Print("Webhook target registration confirmation deferred; durable marker will be reconciled")
			}
		}}, nil
	})
	return nil
}
