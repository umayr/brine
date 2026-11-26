package webhooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const operationSchema = `CREATE TABLE IF NOT EXISTS lifecycle_records(id TEXT PRIMARY KEY,base BLOB NOT NULL,started INTEGER NOT NULL DEFAULT 0,terminal BLOB,accepted_sent INTEGER NOT NULL DEFAULT 0,started_sent INTEGER NOT NULL DEFAULT 0,terminal_sent INTEGER NOT NULL DEFAULT 0,updated INTEGER NOT NULL);`

type Operation struct {
	s  *Service
	id string
}

// BeginOperation durably records intent before admission or any operation child.
// Stable request IDs prevent accidental replay by an adapter. Fanout is retried
// independently from execution; a receiver outage cannot change the outcome.
func (s *Service) BeginOperation(ctx context.Context, event Event) (*Operation, error) {
	event.Type = "operation.accepted"
	event.SchemaVersion = 1
	event.OperationID = event.ID
	event.OccurredAt = s.now().Truncate(time.Millisecond)
	if !validEvent(event) {
		return nil, ErrInvalid
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, errStorage
	}
	defer tx.Rollback()
	var count int
	if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM lifecycle_records WHERE id=?", event.ID).Scan(&count) != nil {
		return nil, errStorage
	}
	if count > 0 {
		return nil, ErrConflict
	}
	if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM lifecycle_records").Scan(&count) != nil {
		return nil, errStorage
	}
	if count >= 100000 {
		return nil, ErrCapacity
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO lifecycle_records(id,base,updated) VALUES(?,?,?)", event.ID, marshal(event), encodeTime(s.now())); e != nil {
		return nil, errStorage
	}
	if tx.Commit() != nil {
		return nil, errStorage
	}
	return &Operation{s, event.ID}, nil
}
func (o *Operation) Started(ctx context.Context) error {
	res, e := o.s.db.ExecContext(ctx, "UPDATE lifecycle_records SET started=?,updated=? WHERE id=? AND started=0 AND terminal IS NULL", encodeTime(o.s.now()), encodeTime(o.s.now()), o.id)
	if e != nil {
		return errStorage
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	return nil
}
func (o *Operation) Finish(ctx context.Context, kind, outcome, effects, reason string) error {
	if !contains([]string{"operation.completed", "operation.outcome_unknown", "operation.rejected"}, kind) {
		return ErrInvalid
	}
	var b []byte
	var event Event
	e := o.s.db.QueryRowContext(ctx, "SELECT base FROM lifecycle_records WHERE id=?", o.id).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil || json.Unmarshal(b, &event) != nil {
		return errStorage
	}
	event.ID = o.id + ":terminal"
	event.Type = kind
	event.Outcome = outcome
	event.Effects = effects
	event.Reason = reason
	event.OccurredAt = o.s.now()
	if !validEvent(event) {
		return ErrInvalid
	}
	res, e := o.s.db.ExecContext(ctx, "UPDATE lifecycle_records SET terminal=?,updated=? WHERE id=? AND terminal IS NULL", marshal(event), encodeTime(o.s.now()), o.id)
	if e != nil {
		return errStorage
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Service) recoverOperations(ctx context.Context) error {
	rows, e := s.db.QueryContext(ctx, "SELECT id,base,started FROM lifecycle_records WHERE terminal IS NULL")
	if e != nil {
		return errStorage
	}
	type row struct {
		id      string
		base    []byte
		started int64
	}
	pending := []row{}
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.base, &r.started) != nil {
			rows.Close()
			return errStorage
		}
		pending = append(pending, r)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return errStorage
	}
	for _, r := range pending {
		op := &Operation{s, r.id}
		kind, outcome, effects, reason := "operation.rejected", "unavailable", "none", "restart_before_execution"
		if r.started != 0 {
			kind, outcome, effects, reason = "operation.outcome_unknown", "unknown", "unknown", "restart_requires_inspection"
		}
		if e = op.Finish(ctx, kind, outcome, effects, reason); e != nil {
			return e
		}
	}
	return nil
}

// reconcileOperations closes the commit-to-fanout crash window without replaying
// the underlying action. Each transition has an immutable deterministic ID.
func (s *Service) reconcileOperations(ctx context.Context) error {
	if e := s.reconcileRejectedRequests(ctx); e != nil {
		return e
	}
	rows, e := s.db.QueryContext(ctx, "SELECT id,base,started,terminal,accepted_sent,started_sent,terminal_sent FROM lifecycle_records WHERE accepted_sent=0 OR (started>0 AND started_sent=0) OR (terminal IS NOT NULL AND terminal_sent=0) ORDER BY updated LIMIT 100")
	if e != nil {
		return errStorage
	}
	type row struct {
		id                                      string
		base, terminal                          []byte
		started                                 int64
		acceptedSent, startedSent, terminalSent bool
	}
	batch := []row{}
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.base, &r.started, &r.terminal, &r.acceptedSent, &r.startedSent, &r.terminalSent) != nil {
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
		var base Event
		if json.Unmarshal(r.base, &base) != nil {
			return errStorage
		}
		events := []struct {
			event  Event
			column string
		}{}
		if !r.acceptedSent {
			accepted := base
			accepted.ID = r.id + ":accepted"
			events = append(events, struct {
				event  Event
				column string
			}{accepted, "accepted_sent"})
		}
		if r.started > 0 && !r.startedSent {
			started := base
			started.ID = r.id + ":started"
			started.Type = "operation.started"
			started.OccurredAt = time.UnixMilli(r.started).UTC()
			events = append(events, struct {
				event  Event
				column string
			}{started, "started_sent"})
		}
		if len(r.terminal) > 0 && !r.terminalSent {
			var terminal Event
			if json.Unmarshal(r.terminal, &terminal) != nil {
				return errStorage
			}
			events = append(events, struct {
				event  Event
				column string
			}{terminal, "terminal_sent"})
		}
		for _, item := range events {
			if e = s.Publish(ctx, item.event); e != nil {
				if errors.Is(e, ErrCapacity) {
					return nil
				}
				return e
			}
			if _, e = s.db.ExecContext(ctx, "UPDATE lifecycle_records SET "+item.column+"=1 WHERE id=?", r.id); e != nil {
				return errStorage
			}
		}
	}
	return nil
}
