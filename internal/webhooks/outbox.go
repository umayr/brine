package webhooks

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
func validEvent(e Event) bool {
	return e.ID != "" && len(e.ID) <= 256 && e.SchemaVersion == 1 && knownEvent(e.Type) && !e.OccurredAt.IsZero() && len(e.OperationID) <= 256 && len(e.Action) <= 64 && len(e.Outcome) <= 128 && len(e.Reason) <= 128 && len(marshal(e)) <= 4096
}

func (s *Service) Publish(ctx context.Context, event Event) error { return s.publish(ctx, event, "") }
func (s *Service) publish(ctx context.Context, event Event, only string) error {
	if !validEvent(event) {
		return ErrInvalid
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return errStorage
	}
	defer tx.Rollback()
	payload := marshal(event)
	var previous []byte
	e = tx.QueryRowContext(ctx, "SELECT payload FROM events WHERE id=?", event.ID).Scan(&previous)
	if e == nil {
		if bytes.Equal(previous, payload) {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return errStorage
	}
	var total int
	if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM events").Scan(&total) != nil {
		return errStorage
	}
	if total >= 100000 {
		return ErrCapacity
	}
	rows, e := tx.QueryContext(ctx, "SELECT config,secret FROM endpoints")
	if e != nil {
		return errStorage
	}
	type dest struct {
		ep     Endpoint
		secret []byte
	}
	destinations := []dest{}
	for rows.Next() {
		var b, secret []byte
		var ep Endpoint
		if rows.Scan(&b, &secret) != nil || json.Unmarshal(b, &ep) != nil {
			rows.Close()
			return errStorage
		}
		if (only == ep.ID || only == "") && ep.Enabled && (only != "" || contains(ep.Events, event.Type)) && (len(ep.Stacks) == 0 || only != "" || contains(ep.Stacks, event.Stack.ID)) {
			destinations = append(destinations, dest{ep, secret})
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return errStorage
	}
	if only != "" && len(destinations) == 0 {
		return ErrConflict
	}
	var pending int
	if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM deliveries WHERE status IN ('pending','sending')").Scan(&pending) != nil {
		return errStorage
	}
	if pending+len(destinations) > s.cfg.MaxPending {
		return ErrCapacity
	}
	var retained int
	if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM deliveries").Scan(&retained) != nil {
		return errStorage
	}
	if retained+len(destinations) > 100000 {
		return ErrCapacity
	}
	now := encodeTime(s.now())
	if _, e = tx.ExecContext(ctx, "INSERT INTO events(id,payload,created) VALUES(?,?,?)", event.ID, payload, now); e != nil {
		return errStorage
	}
	for _, d := range destinations {
		if _, e = tx.ExecContext(ctx, "INSERT INTO deliveries(id,endpoint_id,event_id,url,secret,status,next_at,created,updated) VALUES(?,?,?,?,?,'pending',?,?,?)", newID("del_"), d.ep.ID, event.ID, d.ep.URL, d.secret, now, now, now); e != nil {
			return errStorage
		}
	}
	if tx.Commit() != nil {
		return errStorage
	}
	return nil
}

func (s *Service) Test(ctx context.Context, id string) (Delivery, error) {
	ep, _, e := s.endpoint(ctx, id)
	if e != nil {
		return Delivery{}, e
	}
	if !ep.Enabled {
		return Delivery{}, ErrConflict
	}
	event := Event{ID: newID("evt_"), SchemaVersion: 1, Type: "webhook.test", OccurredAt: s.now(), Reason: "operator_test"}
	if e = s.publish(ctx, event, id); e != nil {
		return Delivery{}, e
	}
	d, e := scanDelivery(s.db.QueryRowContext(ctx, "SELECT "+deliveryColumns+" FROM deliveries d JOIN events e ON e.id=d.event_id WHERE d.endpoint_id=? AND d.event_id=?", id, event.ID))
	if e != nil {
		return Delivery{}, errStorage
	}
	return d, nil
}

const deliveryColumns = "d.id,d.endpoint_id,e.payload,d.status,d.attempts,d.http_status,d.error_category,d.next_at,d.created,d.updated"

type rowScanner interface{ Scan(...any) error }

func scanDelivery(row rowScanner) (Delivery, error) {
	var d Delivery
	var b []byte
	var next, created, updated int64
	e := row.Scan(&d.ID, &d.EndpointID, &b, &d.Status, &d.Attempts, &d.HTTPStatus, &d.ErrorCategory, &next, &created, &updated)
	if e != nil {
		return d, e
	}
	if json.Unmarshal(b, &d.Event) != nil {
		return d, errStorage
	}
	if d.Status == "pending" {
		d.NextAttemptAt = time.UnixMilli(next).UTC()
	}
	d.CreatedAt = time.UnixMilli(created).UTC()
	d.UpdatedAt = time.UnixMilli(updated).UTC()
	return d, nil
}

func (s *Service) Deliveries(ctx context.Context, endpointID string) ([]Delivery, error) {
	query := "SELECT " + deliveryColumns + " FROM deliveries d JOIN events e ON e.id=d.event_id"
	args := []any{}
	if endpointID != "" {
		query += " WHERE d.endpoint_id=?"
		args = append(args, endpointID)
	}
	query += " ORDER BY d.created DESC,d.id LIMIT 200"
	rows, e := s.db.QueryContext(ctx, query, args...)
	if e != nil {
		return nil, errStorage
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		d, e := scanDelivery(rows)
		if e != nil {
			return nil, errStorage
		}
		out = append(out, d)
	}
	if rows.Err() != nil {
		return nil, errStorage
	}
	return out, nil
}
func (s *Service) Attempts(ctx context.Context, id string) ([]Attempt, error) {
	var exists int
	e := s.db.QueryRowContext(ctx, "SELECT 1 FROM deliveries WHERE id=?", id).Scan(&exists)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if e != nil {
		return nil, errStorage
	}
	rows, e := s.db.QueryContext(ctx, "SELECT number,at,duration,http_status,error_category FROM attempts WHERE delivery_id=? ORDER BY number LIMIT 100", id)
	if e != nil {
		return nil, errStorage
	}
	defer rows.Close()
	out := []Attempt{}
	for rows.Next() {
		var a Attempt
		var at int64
		if rows.Scan(&a.Number, &at, &a.DurationMS, &a.HTTPStatus, &a.ErrorCategory) != nil {
			return nil, errStorage
		}
		a.At = time.UnixMilli(at).UTC()
		out = append(out, a)
	}
	if rows.Err() != nil {
		return nil, errStorage
	}
	return out, nil
}

// Redeliver preserves immutable event identity but never reuses a retired URL
// or signing credential. Attempt history is retained; the total stays bounded.
func (s *Service) Redeliver(ctx context.Context, id string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return errStorage
	}
	defer tx.Rollback()
	var epID, url, status string
	var secret []byte
	var attempts int
	e = tx.QueryRowContext(ctx, "SELECT endpoint_id,url,status,secret,attempts FROM deliveries WHERE id=?", id).Scan(&epID, &url, &status, &secret, &attempts)
	if errors.Is(e, sql.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return errStorage
	}
	if status == "sending" || status == "pending" || attempts >= 100 {
		return ErrConflict
	}
	var config, currentSecret []byte
	var ep Endpoint
	e = tx.QueryRowContext(ctx, "SELECT config,secret FROM endpoints WHERE id=?", epID).Scan(&config, &currentSecret)
	if errors.Is(e, sql.ErrNoRows) {
		return ErrConflict
	}
	if e != nil || json.Unmarshal(config, &ep) != nil {
		return errStorage
	}
	if !ep.Enabled || ep.URL != url || !bytes.Equal(secret, currentSecret) {
		return ErrConflict
	}
	var pending int
	if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM deliveries WHERE status IN ('pending','sending')").Scan(&pending) != nil {
		return errStorage
	}
	if pending >= s.cfg.MaxPending {
		return ErrCapacity
	}
	now := encodeTime(s.now())
	if _, e = tx.ExecContext(ctx, "UPDATE deliveries SET status='pending',next_at=?,updated=?,http_status=0,error_category='' WHERE id=?", now, now, id); e != nil {
		return errStorage
	}
	if tx.Commit() != nil {
		return errStorage
	}
	return nil
}
