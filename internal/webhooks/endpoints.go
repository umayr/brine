package webhooks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

func knownEvent(id string) bool {
	for _, v := range Catalog {
		if v == id {
			return true
		}
	}
	return false
}
func validEndpoint(ep Endpoint) bool {
	if strings.TrimSpace(ep.Name) == "" || len(ep.Name) > 100 || len(ep.URL) > 2048 || ValidateDestination(ep.URL) != nil || len(ep.Events) == 0 || len(ep.Events) > len(Catalog) || len(ep.Stacks) > 100 {
		return false
	}
	seen := map[string]bool{}
	for _, event := range ep.Events {
		if !knownEvent(event) || seen[event] {
			return false
		}
		seen[event] = true
	}
	for _, stack := range ep.Stacks {
		if stack == "" || len(stack) > 128 {
			return false
		}
	}
	return true
}

func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", errStorage
	}
	return hex.EncodeToString(b), nil
}

// SaveEndpoint returns a signing key only on creation. URL changes cancel old
// queued deliveries; they can never leak historical payloads to a new receiver.
func (s *Service) SaveEndpoint(ctx context.Context, ep Endpoint) (Endpoint, string, error) {
	if !validEndpoint(ep) {
		return Endpoint{}, "", ErrInvalid
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Endpoint{}, "", errStorage
	}
	defer tx.Rollback()
	now := s.now()
	secret := ""
	var sealed []byte
	if ep.ID == "" {
		if ep.Version != 0 {
			return Endpoint{}, "", ErrConflict
		}
		var count int
		if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM endpoints").Scan(&count) != nil {
			return Endpoint{}, "", errStorage
		}
		if count >= 100 {
			return Endpoint{}, "", ErrCapacity
		}
		ep.ID = newID("wh_")
		ep.Version = 1
		ep.CreatedAt = now
		secret, e = newSecret()
		if e != nil {
			return Endpoint{}, "", e
		}
		sealed, e = s.seal([]byte(secret))
		if e != nil {
			return Endpoint{}, "", e
		}
	} else {
		var b []byte
		var old Endpoint
		e = tx.QueryRowContext(ctx, "SELECT config,secret FROM endpoints WHERE id=?", ep.ID).Scan(&b, &sealed)
		if errors.Is(e, sql.ErrNoRows) {
			return Endpoint{}, "", ErrNotFound
		}
		if e != nil || json.Unmarshal(b, &old) != nil {
			return Endpoint{}, "", errStorage
		}
		if ep.Version != old.Version {
			return Endpoint{}, "", ErrConflict
		}
		ep.Version++
		ep.CreatedAt = old.CreatedAt
		if old.URL != ep.URL {
			secret, e = newSecret()
			if e != nil {
				return Endpoint{}, "", e
			}
			sealed, e = s.seal([]byte(secret))
			if e != nil {
				return Endpoint{}, "", e
			}
			if _, e = tx.ExecContext(ctx, "UPDATE deliveries SET status='cancelled',error_category='destination_changed',updated=? WHERE endpoint_id=? AND status='pending'", encodeTime(now), ep.ID); e != nil {
				return Endpoint{}, "", errStorage
			}
		}
	}
	ep.UpdatedAt = now
	if _, e = tx.ExecContext(ctx, "INSERT INTO endpoints(id,config,secret) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET config=excluded.config,secret=excluded.secret", ep.ID, marshal(ep), sealed); e != nil {
		return Endpoint{}, "", errStorage
	}
	if e = tx.Commit(); e != nil {
		return Endpoint{}, "", errStorage
	}
	return ep, secret, nil
}

func (s *Service) DeleteEndpoint(ctx context.Context, id string, version int64) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return errStorage
	}
	defer tx.Rollback()
	var b []byte
	var ep Endpoint
	e = tx.QueryRowContext(ctx, "SELECT config FROM endpoints WHERE id=?", id).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil || json.Unmarshal(b, &ep) != nil {
		return errStorage
	}
	if ep.Version != version {
		return ErrConflict
	}
	if _, e = tx.ExecContext(ctx, "UPDATE deliveries SET status='cancelled',error_category='endpoint_deleted',updated=? WHERE endpoint_id=? AND status='pending'", encodeTime(s.now()), id); e != nil {
		return errStorage
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM endpoints WHERE id=?", id); e != nil {
		return errStorage
	}
	if tx.Commit() != nil {
		return errStorage
	}
	return nil
}

func (s *Service) RotateSecret(ctx context.Context, id string, version int64) (string, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return "", errStorage
	}
	defer tx.Rollback()
	var b []byte
	var ep Endpoint
	e = tx.QueryRowContext(ctx, "SELECT config FROM endpoints WHERE id=?", id).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if e != nil || json.Unmarshal(b, &ep) != nil {
		return "", errStorage
	}
	if ep.Version != version {
		return "", ErrConflict
	}
	secret, e := newSecret()
	if e != nil {
		return "", e
	}
	sealed, e := s.seal([]byte(secret))
	if e != nil {
		return "", e
	}
	ep.Version++
	ep.UpdatedAt = s.now()
	if _, e = tx.ExecContext(ctx, "UPDATE endpoints SET config=?,secret=? WHERE id=?", marshal(ep), sealed, id); e != nil {
		return "", errStorage
	}
	// Explicit rotation retires old queued credentials instead of retaining
	// recoverable old keys indefinitely. Running attempts may finish with old key.
	if _, e = tx.ExecContext(ctx, "UPDATE deliveries SET status='cancelled',error_category='secret_rotated',updated=? WHERE endpoint_id=? AND status='pending'", encodeTime(s.now()), id); e != nil {
		return "", errStorage
	}
	if tx.Commit() != nil {
		return "", errStorage
	}
	return secret, nil
}
