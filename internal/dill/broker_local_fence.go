package dill

import (
	"context"
	"encoding/json"
	"github.com/umayr/brine/internal/brokerwire"
)

func (c *LocalBroker) Fence(ctx context.Context, journal, id string, ref brokerwire.Reference) (BrokerLocalOutcome, error) {
	if !ref.Valid() || !c.matches(ref.Scope) || !brokerwire.ValidHandle(id) {
		return BrokerLocalOutcome{}, brokerwire.ErrInvalid
	}
	body, _ := json.Marshal(map[string]string{"requestId": id, "stack": ref.Scope.Stack, "confirm": "fence-if-unadmitted"})
	r, err := c.call(ctx, "fence", "", journal, "", body)
	if err != nil {
		return BrokerLocalOutcome{}, err
	}
	return decodeLocalOutcome(r.Body, id, ref, true)
}
