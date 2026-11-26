package webhooks

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// Ingress is mounted outside cookie authentication on an exact /hooks/{id}
// POST route. It performs its own mandatory HMAC authentication. No cookie,
// bearer token, query argument or submitted action can expand the grant.
func (s *Service) Ingress(dispatcher TriggerDispatcher) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		fail := func(code int, message string) {
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(map[string]string{"error": message})
		}
		id := strings.TrimPrefix(r.URL.Path, "/hooks/")
		if r.Method != http.MethodPost || !strings.HasPrefix(id, "trg_") || !validID(id) || r.URL.Path != "/hooks/"+id || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
			fail(404, "not found")
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			fail(415, "expected application/json")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		body, e := io.ReadAll(r.Body)
		if e != nil {
			fail(413, "request body too large")
			return
		}
		accepted, e := s.Accept(r.Context(), id, r.Header.Get("Brine-Timestamp"), r.Header.Get("Brine-Signature"), body, dispatcher)
		if e != nil {
			code := 503
			message := "webhook service unavailable"
			switch {
			case errors.Is(e, ErrUnauthorized), errors.Is(e, ErrNotFound):
				code = 401
				message = "webhook authentication failed"
			case errors.Is(e, ErrInvalid):
				code = 400
				message = "invalid webhook request"
			case errors.Is(e, ErrConflict):
				code = 409
				message = "idempotency key reused with different input or trigger changed"
			case errors.Is(e, ErrBusy):
				code = 409
				message = "stack action already queued or running"
			case errors.Is(e, ErrRateLimited):
				code = 429
				message = "webhook request rate exceeded"
				w.Header().Set("Retry-After", "60")
			case errors.Is(e, ErrUnsupported):
				code = 403
				message = "action unavailable; existing admission and review rules apply"
			}
			fail(code, message)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(accepted)
	})
}

func (s *Service) Accept(ctx context.Context, id, timestamp, signature string, body []byte, dispatcher TriggerDispatcher) (IncomingRequest, error) {
	var zero IncomingRequest
	if len(body) > 4096 || len(timestamp) > 20 || len(signature) > 80 {
		return zero, ErrUnauthorized
	}
	var config, sealed []byte
	var trigger Trigger
	e := s.db.QueryRowContext(ctx, "SELECT config,secret FROM triggers WHERE id=?", id).Scan(&config, &sealed)
	if errors.Is(e, sql.ErrNoRows) {
		return zero, ErrUnauthorized
	}
	if e != nil || json.Unmarshal(config, &trigger) != nil {
		return zero, errStorage
	}
	key, e := s.unseal(sealed)
	if e != nil {
		return zero, errStorage
	}
	defer clear(key)
	if Verify(key, timestamp, signature, body, s.now()) != nil || !trigger.Enabled || !s.now().Before(trigger.ExpiresAt) {
		return zero, ErrUnauthorized
	}
	var input struct {
		ID string `json:"id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || !validID(input.ID) {
		return zero, ErrInvalid
	}
	select {
	case s.incomingAdmission <- struct{}{}:
		defer func() { <-s.incomingAdmission }()
	case <-ctx.Done():
		return zero, ErrBusy
	}
	var refreshed []byte
	if e = s.db.QueryRowContext(ctx, "SELECT config FROM triggers WHERE id=?", id).Scan(&refreshed); e != nil || !bytes.Equal(refreshed, config) || !s.now().Before(trigger.ExpiresAt) {
		return zero, ErrUnauthorized
	}
	digest := requestDigest(body)
	// Authenticated duplicate delivery returns the same durable request, even
	// while the dispatcher is unavailable. It never queues another execution.
	var existing, digestBefore string
	var priorBinding []byte
	e = s.db.QueryRowContext(ctx, "SELECT record,digest,binding FROM incoming_requests WHERE trigger_id=? AND idempotency_key=?", id, input.ID).Scan(&existing, &digestBefore, &priorBinding)
	if e == nil {
		if digest != digestBefore || !bytes.Equal(priorBinding, config) {
			return zero, ErrConflict
		}
		if json.Unmarshal([]byte(existing), &zero) != nil {
			return zero, errStorage
		}
		return zero, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return zero, errStorage
	}
	if dispatcher == nil {
		return zero, ErrUnsupported
	}
	if e = s.reserveValidation(ctx, trigger); e != nil {
		return zero, e
	}
	validateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	validationErr := dispatcher.Validate(validateCtx, trigger)
	cancel()
	if validationErr != nil {
		return zero, ErrUnsupported
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return zero, errStorage
	}
	defer tx.Rollback()
	// Re-read after validation: concurrent changes or revocation must invalidate
	// the authenticated snapshot, not turn it into a newly expanded grant.
	var current []byte
	if tx.QueryRowContext(ctx, "SELECT config FROM triggers WHERE id=?", id).Scan(&current) != nil || !bytes.Equal(current, config) {
		return zero, ErrConflict
	}
	e = tx.QueryRowContext(ctx, "SELECT record,digest,binding FROM incoming_requests WHERE trigger_id=? AND idempotency_key=?", id, input.ID).Scan(&existing, &digestBefore, &priorBinding)
	if e == nil {
		if digest != digestBefore || !bytes.Equal(priorBinding, config) {
			return zero, ErrConflict
		}
		if json.Unmarshal([]byte(existing), &zero) != nil {
			return zero, errStorage
		}
		return zero, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return zero, errStorage
	}
	now := s.now()
	if !now.Before(trigger.ExpiresAt) {
		return zero, ErrUnauthorized
	}
	var count int
	if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM incoming_requests WHERE trigger_id=? AND created>=?", id, encodeTime(now.Add(-time.Minute))).Scan(&count) != nil {
		return zero, errStorage
	}
	if count >= trigger.RequestsPerMinute {
		return zero, ErrRateLimited
	}
	if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM incoming_requests WHERE status IN ('accepted','running','unknown','awaiting_review') AND json_extract(CAST(binding AS TEXT),'$.stack_id')=? AND json_extract(CAST(binding AS TEXT),'$.target_id')=?", trigger.StackID, trigger.TargetID).Scan(&count) != nil {
		return zero, errStorage
	}
	if count > 0 {
		return zero, ErrBusy
	}
	if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM incoming_requests WHERE status IN ('accepted','running')").Scan(&count) != nil {
		return zero, errStorage
	}
	if count >= 1000 {
		return zero, ErrCapacity
	}
	if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM incoming_requests").Scan(&count) != nil {
		return zero, errStorage
	}
	if count >= 100000 {
		return zero, ErrCapacity
	}
	accepted := IncomingRequest{ID: newID("req_"), TriggerID: id, IdempotencyKey: input.ID, Status: "accepted", Binding: trigger, CreatedAt: now, UpdatedAt: now}
	if _, e = tx.ExecContext(ctx, "INSERT INTO incoming_requests(id,trigger_id,idempotency_key,digest,binding,record,status,created,updated) VALUES(?,?,?,?,?,?,'accepted',?,?)", accepted.ID, id, input.ID, digest, config, marshal(accepted), encodeTime(now), encodeTime(now)); e != nil {
		return zero, errStorage
	}
	if tx.Commit() != nil {
		return zero, errStorage
	}
	return accepted, nil
}

func (s *Service) Requests(ctx context.Context, triggerID string) ([]IncomingRequest, error) {
	query := "SELECT record FROM incoming_requests"
	args := []any{}
	if triggerID != "" {
		query += " WHERE trigger_id=?"
		args = append(args, triggerID)
	}
	query += " ORDER BY created DESC,id LIMIT 200"
	rows, e := s.db.QueryContext(ctx, query, args...)
	if e != nil {
		return nil, errStorage
	}
	defer rows.Close()
	out := []IncomingRequest{}
	for rows.Next() {
		var b []byte
		var r IncomingRequest
		if rows.Scan(&b) != nil || json.Unmarshal(b, &r) != nil {
			return nil, errStorage
		}
		out = append(out, r)
	}
	if rows.Err() != nil {
		return nil, errStorage
	}
	return out, nil
}
