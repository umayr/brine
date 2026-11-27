package webhooks

import (
	"context"
	"encoding/json"
)

// UnresolvedRequests pages every blocking record, including retired triggers.
// Stable identity ordering avoids offset skips when a prior page is inspected.
// New records behind the cursor appear on refresh; this is not a frozen snapshot.
func (s *Service) UnresolvedRequests(ctx context.Context, cursor string) ([]IncomingRequest, string, error) {
	if cursor != "" && !validID(cursor) {
		return nil, "", ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, "SELECT record FROM incoming_requests WHERE status IN ('accepted','running','unknown','awaiting_review') AND id>? ORDER BY id LIMIT 201", cursor)
	if err != nil {
		return nil, "", errStorage
	}
	defer rows.Close()
	out := []IncomingRequest{}
	for rows.Next() {
		var raw []byte
		var r IncomingRequest
		if rows.Scan(&raw) != nil || json.Unmarshal(raw, &r) != nil {
			return nil, "", errStorage
		}
		out = append(out, r)
	}
	if rows.Err() != nil {
		return nil, "", errStorage
	}
	next := ""
	if len(out) > 200 {
		out = out[:200]
		next = out[len(out)-1].ID
	}
	return out, next, nil
}
