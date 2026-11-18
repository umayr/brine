package agentclient

import (
	"context"
	"io"
	"net/http"

	"github.com/umayr/brine/internal/dillserve"
)

// Stage transfers an already reviewed snapshot to an opt-in HTTPS receiver.
// It requires the current signed session and does not retry the upload. It does
// not grant permission to evaluate or deploy the uploaded project.
func (c *Client) Stage(ctx context.Context, epoch string, body []byte) (string, error) {
	session, err := c.httpsSession(epoch)
	if ctx == nil || err != nil {
		return "", ErrProtocol
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(session, cancel)
	defer stop()
	return dillserve.StageRequest(ctx, c.baseURL, body, c.do)
}

// Snapshot retrieves and validates the catalog manifest over the verified HTTPS
// session. It does not establish that the manifest matches a particular review.
func (c *Client) Snapshot(ctx context.Context, epoch, stageID string) (dillserve.Manifest, error) {
	var zero dillserve.Manifest
	session, err := c.httpsSession(epoch)
	if ctx == nil || err != nil || !canonicalHex(stageID, 16) {
		return zero, ErrProtocol
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(session, cancel)
	defer stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/snapshots/"+stageID, nil)
	if err != nil {
		return zero, ErrProtocol
	}
	resp, err := c.do(req)
	if err != nil {
		return zero, ErrProtocol
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		return zero, ErrProtocol
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	if err != nil || len(body) > 4<<20 || session.Err() != nil || ctx.Err() != nil {
		return zero, ErrProtocol
	}
	manifest, err := dillserve.ParseCatalog(ctx, stageID, body)
	if err != nil || session.Err() != nil || ctx.Err() != nil {
		return zero, ErrProtocol
	}
	return manifest, nil
}

func (c *Client) httpsSession(epoch string) (context.Context, error) {
	if c.bearer == "" || epoch == "" {
		return nil, ErrProtocol
	}
	c.mu.Lock()
	session := c.epochCtx
	valid := !c.dead && c.epoch == epoch && session != nil && session.Err() == nil
	c.mu.Unlock()
	if !valid {
		return nil, ErrProtocol
	}
	return session, nil
}
