package webhooks

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func (s *Service) recoverIncoming(ctx context.Context) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return errStorage
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, "SELECT record FROM incoming_requests WHERE status='running'")
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
		r.Status = "unknown"
		r.Outcome = "unknown"
		r.Reason = "restart_requires_inspection"
		r.UpdatedAt = s.now()
		out = append(out, r)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return errStorage
	}
	for _, r := range out {
		var terminalBytes []byte
		e = tx.QueryRowContext(ctx, "SELECT terminal FROM lifecycle_records WHERE id=?", r.ID).Scan(&terminalBytes)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return errStorage
		}
		if len(terminalBytes) > 0 {
			var terminal Event
			if json.Unmarshal(terminalBytes, &terminal) != nil || !validEvent(terminal) || terminal.OperationID != r.ID {
				return errStorage
			}
			r.OperationID = terminal.OperationID
			r.Outcome = terminal.Outcome
			r.Reason = terminal.Reason
			switch terminal.Type {
			case "operation.completed":
				r.Status = "completed"
			case "operation.rejected":
				r.Status = "rejected"
				if terminal.Reason == "interactive_review_required" {
					r.Status = "awaiting_review"
				}
			case "operation.outcome_unknown":
				r.Status = "unknown"
			default:
				return errStorage
			}
		}
		if _, e = tx.ExecContext(ctx, "UPDATE incoming_requests SET record=?,status=?,updated=? WHERE id=?", marshal(r), r.Status, encodeTime(r.UpdatedAt), r.ID); e != nil {
			return errStorage
		}
	}
	if tx.Commit() != nil {
		return errStorage
	}
	return nil
}

func (s *Service) RunTriggers(ctx context.Context, dispatcher TriggerDispatcher) error {
	if dispatcher == nil {
		return ErrUnsupported
	}
	s.lifecycle.Lock()
	if s.closed || s.incomingRunning {
		s.lifecycle.Unlock()
		return ErrConflict
	}
	s.incomingRunning = true
	ctx, s.incomingCancel = context.WithCancel(ctx)
	s.incomingDone = make(chan struct{})
	done := s.incomingDone
	s.lifecycle.Unlock()
	defer func() { s.lifecycle.Lock(); close(done); s.incomingRunning = false; s.lifecycle.Unlock() }()
	if ctx.Err() != nil {
		return nil
	}
	if e := s.recoverIncoming(ctx); e != nil {
		if ctx.Err() != nil {
			return nil
		}
		return e
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		executed, e := s.executeOne(ctx, dispatcher)
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		if executed {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Service) claimIncoming(ctx context.Context) (IncomingRequest, Trigger, error) {
	var r IncomingRequest
	var t Trigger
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return r, t, errStorage
	}
	defer tx.Rollback()
	var record, binding, current []byte
	e = tx.QueryRowContext(ctx, "SELECT record,binding FROM incoming_requests WHERE status='accepted' ORDER BY created,id LIMIT 1").Scan(&record, &binding)
	if errors.Is(e, sql.ErrNoRows) {
		return r, t, sql.ErrNoRows
	}
	if e != nil || json.Unmarshal(record, &r) != nil || json.Unmarshal(binding, &t) != nil {
		return r, t, errStorage
	}
	e = tx.QueryRowContext(ctx, "SELECT config FROM triggers WHERE id=?", t.ID).Scan(&current)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return r, t, errStorage
	}
	r.UpdatedAt = s.now()
	r.Status = "running"
	if e != nil || !bytes.Equal(current, binding) || !t.Enabled || !s.now().Before(t.ExpiresAt) {
		r.Status = "cancelled"
		r.Reason = "trigger_revoked"
	}
	if _, e = tx.ExecContext(ctx, "UPDATE incoming_requests SET status=?,record=?,updated=? WHERE id=?", r.Status, marshal(r), encodeTime(r.UpdatedAt), r.ID); e != nil {
		return r, t, errStorage
	}
	if tx.Commit() != nil {
		return r, t, errStorage
	}
	return r, t, nil
}

func safeExecute(ctx context.Context, d TriggerDispatcher, t Trigger, r IncomingRequest) (result TriggerResult) {
	defer func() {
		if recover() != nil {
			result = TriggerResult{Status: "unknown", Outcome: "unknown", Reason: "execution_unconfirmed"}
		}
	}()
	return d.Execute(ctx, t, r)
}
func (s *Service) executeOne(ctx context.Context, d TriggerDispatcher) (bool, error) {
	r, t, e := s.claimIncoming(ctx)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if r.Status == "cancelled" {
		return true, nil
	}
	result := TriggerResult{Status: "rejected", Reason: "admission_denied"}
	runCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	if d.Validate(runCtx, t) == nil {
		admitted, err := s.admitIncoming(runCtx, &r, t)
		if err != nil {
			return true, err
		}
		if !admitted {
			return true, nil
		}
		result = safeExecute(runCtx, d, t, r)
	}
	if !contains([]string{"completed", "awaiting_review", "rejected", "unknown"}, result.Status) {
		result = TriggerResult{Status: "unknown", Outcome: "unknown", Reason: "execution_unconfirmed"}
	}
	if len(result.OperationID) > 256 || len(result.Outcome) > 128 || len(result.Reason) > 128 {
		result = TriggerResult{Status: "unknown", Outcome: "unknown", Reason: "invalid_execution_result"}
	}
	if result.Status == "unknown" {
		result.Outcome = "unknown"
	}
	r.Status = result.Status
	r.OperationID = result.OperationID
	r.Outcome = result.Outcome
	r.Reason = result.Reason
	r.UpdatedAt = s.now()
	saveCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	_, e = s.db.ExecContext(saveCtx, "UPDATE incoming_requests SET status=?,record=?,updated=? WHERE id=? AND status='running'", r.Status, marshal(r), encodeTime(r.UpdatedAt), r.ID)
	if e != nil {
		return true, errStorage
	}
	if r.Status == "rejected" {
		_ = s.recordRejectedRequest(saveCtx, r, t)
	}
	return true, nil
}
