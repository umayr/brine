package brokerapp

import (
	"context"
	"github.com/umayr/brine/internal/dillserve"
)

func (t *LocalTarget) Stage(ctx context.Context, body []byte) (string, error) {
	return t.Client.Stage(ctx, body)
}
func (t *LocalTarget) Snapshot(ctx context.Context, id string) (dillserve.Manifest, error) {
	return t.Client.Snapshot(ctx, id)
}
func (t *HTTPSTarget) Stage(ctx context.Context, body []byte) (string, error) {
	return t.Client.Stage(ctx, t.Epoch, body)
}
func (t *HTTPSTarget) Snapshot(ctx context.Context, id string) (dillserve.Manifest, error) {
	return t.Client.Snapshot(ctx, t.Epoch, id)
}
