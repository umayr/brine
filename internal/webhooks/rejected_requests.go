package webhooks

import (
	"context"
	"encoding/json"
	"errors"
)

func (s *Service) recordRejectedRequest(ctx context.Context, r IncomingRequest, t Trigger) error {
	op, e := s.BeginOperation(ctx, Event{ID: r.ID, Stack: Resource{ID: t.StackID, Kind: "stack"}, Source: Resource{Kind: "unknown"}, Target: Resource{ID: t.TargetID, Kind: "unknown"}, Action: t.Action})
	if errors.Is(e, ErrConflict) {
		return nil
	}
	if e != nil {
		return e
	}
	return op.Finish(ctx, "operation.rejected", "unavailable", "none", r.Reason)
}
func (s *Service) reconcileRejectedRequests(ctx context.Context) error {
	rows, e := s.db.QueryContext(ctx, "SELECT record,binding FROM incoming_requests r WHERE r.status='rejected' AND NOT EXISTS(SELECT 1 FROM lifecycle_records o WHERE o.id=r.id) ORDER BY created LIMIT 100")
	if e != nil {
		return errStorage
	}
	type item struct {
		r IncomingRequest
		t Trigger
	}
	batch := []item{}
	for rows.Next() {
		var b, binding []byte
		var i item
		if rows.Scan(&b, &binding) != nil || json.Unmarshal(b, &i.r) != nil || json.Unmarshal(binding, &i.t) != nil {
			rows.Close()
			return errStorage
		}
		batch = append(batch, i)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return errStorage
	}
	for _, i := range batch {
		if e = s.recordRejectedRequest(ctx, i.r, i.t); e != nil {
			if errors.Is(e, ErrCapacity) {
				return nil
			}
			return e
		}
	}
	return nil
}
