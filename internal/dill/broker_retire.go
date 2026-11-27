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
	var reply brokerwire.PreparedReply
	if err != nil || brokerwire.DecodeMetadata(bytes.NewReader(response.Body), &reply) != nil || !reply.ValidFor(request.Scope, time.Now()) {
		return brokerwire.Review{}, brokerwire.ErrUnavailable
	}
	return reply.Prepared, nil
}
