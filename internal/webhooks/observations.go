package webhooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

const observationSchema = `CREATE TABLE IF NOT EXISTS observations(id TEXT PRIMARY KEY,value BLOB NOT NULL,base BLOB NOT NULL,target TEXT NOT NULL,updated INTEGER NOT NULL);`

// Value is nil unless the source actually reported a value. Empty reported
// strings remain distinct from unavailable and explicitly unconfigured health.
type Observation struct {
	Availability string  `json:"availability"`
	Value        *string `json:"value"`
}
type ObservationChange struct {
	Previous Observation `json:"previous"`
	Current  Observation `json:"current"`
}

func validObservation(v Observation) bool {
	return contains([]string{"reported", "unavailable", "not_configured", "empty"}, v.Availability) && ((v.Availability == "reported" && v.Value != nil && len(*v.Value) <= 128) || (v.Availability != "reported" && v.Value == nil))
}

// Observe atomically advances a baseline and journals its transition. First
// observation establishes a baseline, not a fabricated change. Delivery failure
// cannot roll back runtime state or cause an execution retry.
func (s *Service) Observe(ctx context.Context, key string, base Event, current Observation) error {
	if !validID(key) || !validObservation(current) || !contains([]string{"container.state_changed", "container.health_changed", "stack.health_changed", "target.connectivity_changed", "target.capabilities_changed"}, base.Type) {
		return ErrInvalid
	}
	check := base
	check.ID = key
	check.SchemaVersion = 1
	check.OccurredAt = s.now()
	if !validEvent(check) || !validID(base.Target.ID) {
		return ErrInvalid
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return errStorage
	}
	defer tx.Rollback()
	var previous []byte
	e = tx.QueryRowContext(ctx, "SELECT value FROM observations WHERE id=?", key).Scan(&previous)
	first := errors.Is(e, sql.ErrNoRows)
	if e != nil && !first {
		return errStorage
	}
	now := encodeTime(s.now())
	payload := marshal(current)
	if !first {
		var old Observation
		if json.Unmarshal(previous, &old) != nil || !validObservation(old) {
			return errStorage
		}
		if string(previous) != string(payload) {
			base.ID = newID("obs_")
			base.SchemaVersion = 1
			base.OccurredAt = s.now()
			base.Observation = &ObservationChange{Previous: old, Current: current}
			if !validEvent(base) {
				return ErrInvalid
			}
			if _, e = tx.ExecContext(ctx, "DELETE FROM metadata_mutations WHERE state IN ('published','aborted')"); e != nil {
				return errStorage
			}
			var count int
			if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM metadata_mutations").Scan(&count) != nil {
				return errStorage
			}
			if count >= 100000 {
				return ErrCapacity
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO metadata_mutations(id,name,stack_id,before_head,kind,events,state,created,updated) VALUES(?,?,?,'','observation',?,'committed',?,?)", base.ID, key, base.Target.ID, marshal([]Event{base}), now, now); e != nil {
				return errStorage
			}
		}
	} else {
		var count int
		if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM observations").Scan(&count) != nil {
			return errStorage
		}
		if count >= 100000 {
			return ErrCapacity
		}
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO observations(id,value,base,target,updated) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value,base=excluded.base,updated=excluded.updated", key, payload, marshal(base), base.Target.ID, now); e != nil {
		return errStorage
	}
	if tx.Commit() != nil {
		return errStorage
	}
	return nil
}
