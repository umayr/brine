package webhooks

import (
	"context"
	"encoding/json"
)

// MarkUnobserved invalidates old readings, never inventing stopped/removed or
// successful empty inventory. seen is provided only after a complete sample.
func (s *Service) MarkUnobserved(ctx context.Context, target string, seen map[string]bool) error {
	rows, e := s.db.QueryContext(ctx, "SELECT id,base,value FROM observations WHERE target=?", target)
	if e != nil {
		return errStorage
	}
	type item struct {
		id   string
		base Event
	}
	items := []item{}
	for rows.Next() {
		var key string
		var body, value []byte
		var base Event
		var current Observation
		if rows.Scan(&key, &body, &value) != nil || json.Unmarshal(body, &base) != nil || json.Unmarshal(value, &current) != nil || !validObservation(current) {
			rows.Close()
			return errStorage
		}
		if current.Availability != "unavailable" && base.Type != "target.connectivity_changed" && base.Type != "target.capabilities_changed" && !seen[key] {
			items = append(items, item{key, base})
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return errStorage
	}
	for _, i := range items {
		if e = s.Observe(ctx, i.id, i.base, Observation{Availability: "unavailable"}); e != nil {
			return e
		}
	}
	return nil
}
