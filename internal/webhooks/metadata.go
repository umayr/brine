package webhooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

const TargetRegistryScope = "registry:"

const metadataSchema = `CREATE TABLE IF NOT EXISTS metadata_mutations(id TEXT PRIMARY KEY,name TEXT NOT NULL,stack_id TEXT NOT NULL,before_head TEXT NOT NULL,kind TEXT NOT NULL,events BLOB NOT NULL,state TEXT NOT NULL,created INTEGER NOT NULL,updated INTEGER NOT NULL);CREATE INDEX IF NOT EXISTS metadata_pending ON metadata_mutations(state,created);`

type MetadataObservation struct {
	Exists        bool
	StackID, Head string
}
type MetadataObservationFunc func(context.Context, string) (MetadataObservation, error)

func (s *Service) ConfigureMetadataObservation(observe MetadataObservationFunc) error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.running.Load() || s.closed {
		return ErrConflict
	}
	s.metadataObserve = observe
	return nil
}

// BeginMetadataMutation journals sanitized intent before the metadata commit.
// The previous metadata head proves its predecessor committed, closing the
// callback/crash window before another mutation can replace that evidence.
func (s *Service) BeginMetadataMutation(ctx context.Context, name, stackID, beforeHead, kind string, events []Event) (string, error) {
	if !validID(name) || !validID(stackID) || (beforeHead != "" && !validID(beforeHead)) || !contains([]string{"write", "delete", "restore", "registry"}, kind) || len(events) == 0 || len(events) > 8 {
		return "", ErrInvalid
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return "", errStorage
	}
	defer tx.Rollback()
	if beforeHead != "" {
		if e = s.confirmMetadataTx(ctx, tx, beforeHead, stackID, true); e != nil && !errors.Is(e, ErrNotFound) {
			return "", e
		}
	}
	// Terminal journal rows no longer carry recovery evidence: their immutable
	// events are already in the outbox/seen ledger. Do not let them block source
	// mutations while waiting for the delivery-history retention deadline.
	if _, e = tx.ExecContext(ctx, "DELETE FROM metadata_mutations WHERE state IN ('published','aborted')"); e != nil {
		return "", errStorage
	}
	var count int
	if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM metadata_mutations WHERE name=? AND state='prepared'", name).Scan(&count) != nil {
		return "", errStorage
	}
	if count > 0 {
		return "", ErrConflict
	}
	if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM metadata_mutations").Scan(&count) != nil {
		return "", errStorage
	}
	if count >= 100000 {
		return "", ErrCapacity
	}
	id := newID("meta_")
	now := s.now()
	for i := range events {
		if kind == "registry" {
			if !strings.HasPrefix(name, TargetRegistryScope) || len(name) != len(TargetRegistryScope)+64 || stackID != name || events[i].Stack.ID != "" || !validID(events[i].Target.ID) || !contains([]string{"target.enrolled", "target.updated", "target.removed"}, events[i].Type) {
				return "", ErrInvalid
			}
		} else if events[i].Stack.ID != stackID || (events[i].Stack.Name != "" && events[i].Stack.Name != name) {
			return "", ErrInvalid
		}
		events[i].ID = id + ":" + string(rune('0'+i))
		events[i].SchemaVersion = 1
		events[i].OccurredAt = now
		if !validEvent(events[i]) {
			return "", ErrInvalid
		}
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO metadata_mutations(id,name,stack_id,before_head,kind,events,state,created,updated) VALUES(?,?,?,?,?,?,'prepared',?,?)", id, name, stackID, beforeHead, kind, marshal(events), encodeTime(now), encodeTime(now)); e != nil {
		return "", errStorage
	}
	if tx.Commit() != nil {
		return "", errStorage
	}
	return id, nil
}
func (s *Service) confirmMetadataTx(ctx context.Context, tx *sql.Tx, id, stackID string, recovered bool) error {
	var b []byte
	var state string
	e := tx.QueryRowContext(ctx, "SELECT events,state FROM metadata_mutations WHERE id=? AND stack_id=?", id, stackID).Scan(&b, &state)
	if errors.Is(e, sql.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return errStorage
	}
	if state == "committed" || state == "published" {
		return nil
	}
	if state != "prepared" {
		return ErrConflict
	}
	var events []Event
	if json.Unmarshal(b, &events) != nil {
		return errStorage
	}
	now := s.now()
	for i := range events {
		events[i].OccurredAt = now
		if recovered {
			events[i].Reconciled = true
			if events[i].Reason == "" {
				events[i].Reason = "reconciled_metadata_commit"
			}
		}
	}
	if _, e = tx.ExecContext(ctx, "UPDATE metadata_mutations SET state='committed',events=?,updated=? WHERE id=? AND state='prepared'", marshal(events), encodeTime(now), id); e != nil {
		return errStorage
	}
	return nil
}
func (s *Service) ConfirmMetadataMutation(ctx context.Context, id, stackID string) error {
	return s.confirmMetadata(ctx, id, stackID, false)
}
func (s *Service) confirmMetadata(ctx context.Context, id, stackID string, recovered bool) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return errStorage
	}
	defer tx.Rollback()
	if e = s.confirmMetadataTx(ctx, tx, id, stackID, recovered); e != nil {
		return e
	}
	if tx.Commit() != nil {
		return errStorage
	}
	return nil
}

// ReconcileMetadata never treats unreadable metadata as deletion or success.
// Read callbacks execute without any DB transaction, avoiding store/DB inversion.
func (s *Service) ReconcileMetadata(ctx context.Context) error {
	if s.metadataObserve != nil {
		rows, e := s.db.QueryContext(ctx, "SELECT id,name,stack_id,before_head,kind FROM metadata_mutations WHERE state='prepared' ORDER BY updated,id LIMIT 100")
		if e != nil {
			return errStorage
		}
		type record struct{ id, name, stackID, before, kind string }
		batch := []record{}
		for rows.Next() {
			var r record
			if rows.Scan(&r.id, &r.name, &r.stackID, &r.before, &r.kind) != nil {
				rows.Close()
				return errStorage
			}
			batch = append(batch, r)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return errStorage
		}
		for _, r := range batch {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			seen, e := s.metadataObserve(ctx, r.name)
			if e != nil {
				if _, err := s.db.ExecContext(ctx, "UPDATE metadata_mutations SET updated=? WHERE id=? AND state='prepared'", encodeTime(s.now()), r.id); err != nil {
					return errStorage
				}
				continue
			}
			committed := r.kind == "delete" && !seen.Exists || r.kind != "delete" && seen.Exists && seen.StackID == r.stackID && seen.Head == r.id
			if committed {
				if e = s.confirmMetadata(ctx, r.id, r.stackID, true); e != nil && !errors.Is(e, ErrConflict) {
					return e
				}
				continue
			}
			aborted := r.kind == "delete" && seen.Exists && seen.StackID == r.stackID && seen.Head == r.before || r.kind != "delete" && (!seen.Exists || seen.StackID == r.stackID && seen.Head == r.before)
			if aborted {
				if _, e = s.db.ExecContext(ctx, "UPDATE metadata_mutations SET state='aborted',updated=? WHERE id=? AND state='prepared'", encodeTime(s.now()), r.id); e != nil {
					return errStorage
				}
			} else {
				if _, e = s.db.ExecContext(ctx, "UPDATE metadata_mutations SET updated=? WHERE id=? AND state='prepared'", encodeTime(s.now()), r.id); e != nil {
					return errStorage
				}
			}
		}
	}
	rows, e := s.db.QueryContext(ctx, "SELECT id,events FROM metadata_mutations WHERE state='committed' ORDER BY created LIMIT 100")
	if e != nil {
		return errStorage
	}
	type committed struct {
		id     string
		events []Event
	}
	out := []committed{}
	for rows.Next() {
		var id string
		var b []byte
		var events []Event
		if rows.Scan(&id, &b) != nil || json.Unmarshal(b, &events) != nil {
			rows.Close()
			return errStorage
		}
		out = append(out, committed{id, events})
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return errStorage
	}
	for _, r := range out {
		for _, event := range r.events {
			if e = s.Publish(ctx, event); e != nil {
				if errors.Is(e, ErrCapacity) {
					return nil
				}
				return e
			}
		}
		if _, e = s.db.ExecContext(ctx, "UPDATE metadata_mutations SET state='published' WHERE id=? AND state='committed'", r.id); e != nil {
			return errStorage
		}
	}
	return nil
}
