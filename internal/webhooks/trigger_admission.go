package webhooks

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func (s *Service) reserveValidation(ctx context.Context, t Trigger) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return errStorage
	}
	defer tx.Rollback()
	var busy int
	if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM incoming_requests WHERE status IN ('accepted','running','unknown','awaiting_review') AND json_extract(CAST(binding AS TEXT),'$.stack_id')=? AND json_extract(CAST(binding AS TEXT),'$.target_id')=?", t.StackID, t.TargetID).Scan(&busy) != nil {
		return errStorage
	}
	if busy != 0 {
		return ErrBusy
	}
	var window int64
	var used int
	nowWindow := s.now().Unix() / 60
	e = tx.QueryRowContext(ctx, "SELECT window,used FROM trigger_validation WHERE trigger_id=?", t.ID).Scan(&window, &used)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return errStorage
	}
	if window != nowWindow {
		used = 0
	}
	if used >= t.RequestsPerMinute {
		return ErrRateLimited
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO trigger_validation(trigger_id,window,used) VALUES(?,?,?) ON CONFLICT(trigger_id) DO UPDATE SET window=excluded.window,used=excluded.used", t.ID, nowWindow, used+1); e != nil {
		return errStorage
	}
	if tx.Commit() != nil {
		return errStorage
	}
	return nil
}

// admitIncoming is the dispatch linearization point, AFTER external validation.
// Revocation/expiry before this transaction prevents execution. Once durably
// admitted, an in-flight action may finish, but it is never replayed.
func (s *Service) admitIncoming(ctx context.Context, r *IncomingRequest, t Trigger) (bool, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return false, errStorage
	}
	defer tx.Rollback()
	var current []byte
	e = tx.QueryRowContext(ctx, "SELECT config FROM triggers WHERE id=?", t.ID).Scan(&current)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return false, errStorage
	}
	allowed := e == nil && bytes.Equal(current, marshal(t)) && t.Enabled && s.now().Before(t.ExpiresAt)
	r.UpdatedAt = s.now()
	if allowed {
		r.Reason = "dispatch_admitted"
	} else {
		r.Status = "cancelled"
		r.Reason = "trigger_revoked"
	}
	if _, e = tx.ExecContext(ctx, "UPDATE incoming_requests SET status=?,record=?,updated=? WHERE id=? AND status='running'", r.Status, marshal(r), encodeTime(r.UpdatedAt), r.ID); e != nil {
		return false, errStorage
	}
	if tx.Commit() != nil {
		return false, errStorage
	}
	return allowed, nil
}

// ResolveRequest releases an uncertain binding only after an authenticated
// operator has independently inspected the target. It does not change the
// reported outcome or cause an execution. The caller must require review/CSRF.
func (s *Service) ResolveRequest(ctx context.Context, id string, expectedUpdated time.Time) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return errStorage
	}
	defer tx.Rollback()
	var b []byte
	var r IncomingRequest
	e = tx.QueryRowContext(ctx, "SELECT record FROM incoming_requests WHERE id=?", id).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil || json.Unmarshal(b, &r) != nil {
		return errStorage
	}
	if (r.Status != "unknown" && r.Status != "awaiting_review") || !r.UpdatedAt.Equal(expectedUpdated) {
		return ErrConflict
	}
	r.Status = "resolved"
	r.Reason = "operator_inspected"
	r.UpdatedAt = s.now()
	if _, e = tx.ExecContext(ctx, "UPDATE incoming_requests SET status=?,record=?,updated=? WHERE id=?", r.Status, marshal(r), encodeTime(r.UpdatedAt), id); e != nil {
		return errStorage
	}
	if tx.Commit() != nil {
		return errStorage
	}
	return nil
}
