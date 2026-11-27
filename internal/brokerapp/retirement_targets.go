package brokerapp

import (
	"context"
	"github.com/umayr/brine/internal/brokerwire"
)

func (t *LocalTarget) Retire(ctx context.Context, journal, stage string, r brokerwire.Retirement) (brokerwire.Review, error) {
	return t.Client.Retire(ctx, journal, stage, r)
}
func (t *HTTPSTarget) Retire(ctx context.Context, journal, stage string, r brokerwire.Retirement) (brokerwire.Review, error) {
	return t.Client.Retire(ctx, t.Epoch, journal, stage, r)
}
