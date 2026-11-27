package agentclient

import (
	"context"
	"github.com/umayr/brine/internal/brokerwire"
	"time"
)

func (c *BrokerClient) Retire(ctx context.Context, epoch, journal, stage string, request brokerwire.Retirement) (brokerwire.Review, error) {
	if !canonicalHex(stage, 16) || !request.Valid() || !c.matches(request.Scope) {
		return brokerwire.Review{}, ErrProtocol
	}
	body, err := brokerwire.EncodeRetirement(request)
	if err != nil {
		return brokerwire.Review{}, ErrProtocol
	}
	raw, _, err := c.call(ctx, epoch, "POST", "/v1/broker-secrets/retire/"+stage, journal, body, 35*time.Second)
	var reply struct {
		Contract string            `json:"contract"`
		Prepared brokerwire.Review `json:"prepared"`
	}
	if err != nil || decodeStrict(raw, &reply) != nil || reply.Contract != brokerwire.Version || reply.Prepared.Scope != request.Scope || reply.Prepared.Active || !brokerwire.ValidHandle(reply.Prepared.Handle) || !reply.Prepared.ExpiresAt.After(time.Now()) || reply.Prepared.ExpiresAt.After(time.Now().Add(5*time.Minute)) {
		return brokerwire.Review{}, ErrProtocol
	}
	return reply.Prepared, nil
}
