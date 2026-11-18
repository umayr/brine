package agentclient

import (
	"context"

	"github.com/umayr/brine/internal/dillserve"
)

// Stage transfers an already reviewed snapshot to an opt-in HTTPS receiver.
// It requires the current signed session and does not retry the upload. It does
// not grant permission to evaluate or deploy the uploaded project.
func (c *Client) Stage(ctx context.Context, epoch string, body []byte) (string, error) {
	if ctx == nil || c.bearer == "" || epoch == "" {
		return "", ErrProtocol
	}
	c.mu.Lock()
	session := c.epochCtx
	valid := !c.dead && c.epoch == epoch && session != nil && session.Err() == nil
	c.mu.Unlock()
	if !valid {
		return "", ErrProtocol
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(session, cancel)
	defer stop()
	return dillserve.StageRequest(ctx, c.baseURL, body, c.do)
}
