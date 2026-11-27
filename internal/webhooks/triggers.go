package webhooks

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrUnauthorized = errors.New("webhook authentication failed")
	ErrRateLimited  = errors.New("webhook request rate exceeded")
	ErrBusy         = errors.New("webhook action already queued or running")
	ErrUnsupported  = errors.New("webhook action unavailable for this stack and target")
)
var Actions = []string{"plan", "git-sync", "deploy", "start", "stop", "restart"}

type Trigger struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	StackID           string    `json:"stack_id"`
	TargetID          string    `json:"target_id"`
	Action            string    `json:"action"`
	Enabled           bool      `json:"enabled"`
	RequestsPerMinute int       `json:"requests_per_minute"`
	ExpiresAt         time.Time `json:"expires_at"`
	Version           int64     `json:"version"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type IncomingRequest struct {
	Binding        Trigger   `json:"binding"`
	ID             string    `json:"id"`
	TriggerID      string    `json:"trigger_id"`
	IdempotencyKey string    `json:"idempotency_key"`
	Status         string    `json:"status"`
	OperationID    string    `json:"operation_id,omitempty"`
	Outcome        string    `json:"outcome,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// TriggerDispatcher MUST reapply ownership/protected-state/capability/approval
// admission at execution. It cannot create browser identities or bypass review.
// Execute is invoked at most once per durable accepted request. An uncertain
// result must be unknown, never automatically retried.
type TriggerDispatcher interface {
	Validate(context.Context, Trigger) error
	Execute(context.Context, Trigger, IncomingRequest) TriggerResult
}
type TriggerResult struct{ Status, OperationID, Outcome, Reason string }

const triggerSchema = `
CREATE TABLE IF NOT EXISTS triggers(id TEXT PRIMARY KEY,config BLOB NOT NULL,secret BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS incoming_requests(
 id TEXT PRIMARY KEY,trigger_id TEXT NOT NULL,idempotency_key TEXT NOT NULL,
 digest TEXT NOT NULL,binding BLOB NOT NULL,record BLOB NOT NULL,
 status TEXT NOT NULL,created INTEGER NOT NULL,updated INTEGER NOT NULL,
 UNIQUE(trigger_id,idempotency_key));
CREATE INDEX IF NOT EXISTS incoming_queue ON incoming_requests(status,created);
CREATE TABLE IF NOT EXISTS trigger_validation(trigger_id TEXT PRIMARY KEY,window INTEGER NOT NULL,used INTEGER NOT NULL);
`

func (s *Service) Triggers(ctx context.Context) ([]Trigger, error) {
	rows, e := s.db.QueryContext(ctx, "SELECT config FROM triggers ORDER BY id LIMIT 100")
	if e != nil {
		return nil, errStorage
	}
	defer rows.Close()
	out := []Trigger{}
	for rows.Next() {
		var b []byte
		var t Trigger
		if rows.Scan(&b) != nil || json.Unmarshal(b, &t) != nil {
			return nil, errStorage
		}
		out = append(out, t)
	}
	if rows.Err() != nil {
		return nil, errStorage
	}
	return out, nil
}
func validID(v string) bool {
	if len(v) == 0 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' || c == ':') {
			return false
		}
	}
	return true
}

func (s *Service) SaveTrigger(ctx context.Context, t Trigger) (Trigger, string, error) {
	if !validID(t.StackID) || !validID(t.TargetID) || !contains(Actions, t.Action) || len(t.Name) == 0 || len(t.Name) > 100 {
		return Trigger{}, "", ErrInvalid
	}
	now := s.now()
	if t.ExpiresAt.IsZero() {
		t.ExpiresAt = now.Add(30 * 24 * time.Hour)
	}
	if !t.ExpiresAt.After(now) || t.ExpiresAt.After(now.Add(90*24*time.Hour)) {
		return Trigger{}, "", ErrInvalid
	}
	if t.RequestsPerMinute == 0 {
		t.RequestsPerMinute = 5
	}
	if t.RequestsPerMinute < 1 || t.RequestsPerMinute > 60 {
		return Trigger{}, "", ErrInvalid
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Trigger{}, "", errStorage
	}
	defer tx.Rollback()
	var sealed []byte
	secret := ""
	if t.ID == "" {
		if t.Version != 0 {
			return Trigger{}, "", ErrConflict
		}
		var count int
		if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM triggers").Scan(&count) != nil {
			return Trigger{}, "", errStorage
		}
		if count >= 100 {
			return Trigger{}, "", ErrCapacity
		}
		t.ID = newID("trg_")
		t.Version = 1
		t.CreatedAt = now
		secret, e = newSecret()
		if e != nil {
			return Trigger{}, "", e
		}
		sealed, e = s.seal([]byte(secret))
		if e != nil {
			return Trigger{}, "", e
		}
	} else {
		var b []byte
		var old Trigger
		e = tx.QueryRowContext(ctx, "SELECT config,secret FROM triggers WHERE id=?", t.ID).Scan(&b, &sealed)
		if errors.Is(e, sql.ErrNoRows) {
			return Trigger{}, "", ErrNotFound
		}
		if e != nil || json.Unmarshal(b, &old) != nil {
			return Trigger{}, "", errStorage
		}
		if old.Version != t.Version {
			return Trigger{}, "", ErrConflict
		}
		// A credential may not silently gain a different action, stack, or target.
		// Rebinding rotates the key and invalidates the earlier queued grant.
		if old.StackID != t.StackID || old.TargetID != t.TargetID || old.Action != t.Action {
			secret, e = newSecret()
			if e != nil {
				return Trigger{}, "", e
			}
			sealed, e = s.seal([]byte(secret))
			if e != nil {
				return Trigger{}, "", e
			}
		}
		t.Version++
		t.CreatedAt = old.CreatedAt
	}
	t.UpdatedAt = now
	if _, e = tx.ExecContext(ctx, "INSERT INTO triggers(id,config,secret) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET config=excluded.config,secret=excluded.secret", t.ID, marshal(t), sealed); e != nil {
		return Trigger{}, "", errStorage
	}
	if e = cancelQueuedTrigger(ctx, tx, t.ID, now, "trigger_changed"); e != nil {
		return Trigger{}, "", e
	}
	if tx.Commit() != nil {
		return Trigger{}, "", errStorage
	}
	return t, secret, nil
}
func cancelQueuedTrigger(ctx context.Context, tx *sql.Tx, id string, now time.Time, reason string) error {
	rows, e := tx.QueryContext(ctx, "SELECT record FROM incoming_requests WHERE trigger_id=? AND status='accepted'", id)
	if e != nil {
		return errStorage
	}
	out := []IncomingRequest{}
	for rows.Next() {
		var b []byte
		var r IncomingRequest
		if rows.Scan(&b) != nil || json.Unmarshal(b, &r) != nil {
			rows.Close()
			return errStorage
		}
		r.Status = "cancelled"
		r.Reason = reason
		r.UpdatedAt = now
		out = append(out, r)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return errStorage
	}
	for _, r := range out {
		if _, e = tx.ExecContext(ctx, "UPDATE incoming_requests SET status=?,record=?,updated=? WHERE id=?", r.Status, marshal(r), encodeTime(now), r.ID); e != nil {
			return errStorage
		}
	}
	return nil
}
func (s *Service) DeleteTrigger(ctx context.Context, id string, version int64) error {
	return s.changeTriggerKey(ctx, id, version, true, nil)
}
func (s *Service) RotateTriggerSecret(ctx context.Context, id string, version int64) (string, error) {
	secret, e := newSecret()
	if e != nil {
		return "", e
	}
	e = s.changeTriggerKey(ctx, id, version, false, []byte(secret))
	if e != nil {
		return "", e
	}
	return secret, nil
}
func (s *Service) changeTriggerKey(ctx context.Context, id string, version int64, remove bool, key []byte) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return errStorage
	}
	defer tx.Rollback()
	var b []byte
	var t Trigger
	e = tx.QueryRowContext(ctx, "SELECT config FROM triggers WHERE id=?", id).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil || json.Unmarshal(b, &t) != nil {
		return errStorage
	}
	if t.Version != version {
		return ErrConflict
	}
	if remove {
		if _, e = tx.ExecContext(ctx, "DELETE FROM trigger_validation WHERE trigger_id=?", id); e != nil {
			return errStorage
		}
		_, e = tx.ExecContext(ctx, "DELETE FROM triggers WHERE id=?", id)
	} else {
		var sealed []byte
		sealed, e = s.seal(key)
		if e != nil {
			return e
		}
		t.Version++
		t.UpdatedAt = s.now()
		_, e = tx.ExecContext(ctx, "UPDATE triggers SET config=?,secret=? WHERE id=?", marshal(t), sealed, id)
	}
	if e != nil {
		return errStorage
	}
	if e = cancelQueuedTrigger(ctx, tx, id, s.now(), "trigger_revoked"); e != nil {
		return e
	}
	if tx.Commit() != nil {
		return errStorage
	}
	return nil
}
func requestDigest(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }
