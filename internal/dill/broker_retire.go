package dill

import (
	"bytes"
	"context"
	"github.com/umayr/brine/internal/brokerwire"
	"time"
)

func (c *LocalBroker) Retire(ctx context.Context, journal, stage string, request brokerwire.Retirement) (brokerwire.Review, error) {
	if !request.Valid() || !c.matches(request.Scope) || !brokerwire.ValidHandle(stage) {
		return brokerwire.Review{}, brokerwire.ErrInvalid
	}
	body, err := brokerwire.EncodeRetirement(request)
	if err != nil {
		return brokerwire.Review{}, err
	}
	response, err := c.call(ctx, "retire", stage, journal, "", body)
	var reply struct {
		Contract string            `json:"contract"`
		Prepared brokerwire.Review `json:"prepared"`
	}
	if err != nil || brokerwire.DecodeMetadata(bytes.NewReader(response.Body), &reply) != nil || reply.Contract != brokerwire.Version || reply.Prepared.Scope != request.Scope || reply.Prepared.Active || !brokerwire.ValidHandle(reply.Prepared.Handle) || !reply.Prepared.ExpiresAt.After(time.Now()) || reply.Prepared.ExpiresAt.After(time.Now().Add(5*time.Minute)) {
		return brokerwire.Review{}, brokerwire.ErrUnavailable
	}
	return reply.Prepared, nil
}
