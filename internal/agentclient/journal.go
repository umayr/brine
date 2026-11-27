package agentclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// DeploymentJournal discovers the current opaque journal through a separately
// granted deployment read. It is not execution authority or an ownership claim.
func (c *Client) DeploymentJournal(ctx context.Context, epoch string) (string, error) {
	session, e := c.httpsSession(epoch)
	if e != nil {
		return "", e
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	stop := context.AfterFunc(session, cancel)
	defer stop()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/deploy/journal", nil)
	resp, e := c.do(req)
	if e != nil {
		return "", ErrProtocol
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/json" {
		return "", ErrProtocol
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, 513))
	if e != nil || len(b) > 512 || checkJSON(b) != nil || ctx.Err() != nil || session.Err() != nil {
		return "", ErrProtocol
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil || len(fields) != 1 {
		return "", ErrProtocol
	}
	var id string
	if json.Unmarshal(fields["journalId"], &id) != nil || !canonicalHex(id, 16) {
		return "", ErrProtocol
	}
	headers := resp.Header.Values(journalHeader)
	if len(headers) != 1 || headers[0] != id {
		return "", ErrProtocol
	}
	return id, nil
}
