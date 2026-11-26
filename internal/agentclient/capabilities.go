package agentclient

import (
	"context"
	"io"
	"net/http"
	"strings"
)

// Capabilities reads configured route metadata on the exact pinned HTTPS
// session. Deployment-prefixed entries require a different credential and
// explicit host grants; they never authorize this monitor client to execute.
func (c *Client) Capabilities(ctx context.Context, epoch string) ([]string, error) {
	session, e := c.httpsSession(epoch)
	if ctx == nil || e != nil {
		return nil, ErrProtocol
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(session, cancel)
	defer stop()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/capabilities", nil)
	if e != nil {
		return nil, ErrProtocol
	}
	response, e := c.do(req)
	if e != nil {
		return nil, ErrProtocol
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json" {
		return nil, ErrProtocol
	}
	body, e := io.ReadAll(io.LimitReader(response.Body, 32769))
	if e != nil || len(body) > 32768 || ctx.Err() != nil || session.Err() != nil {
		return nil, ErrProtocol
	}
	return parseCapabilities(body)
}
func parseCapabilities(body []byte) ([]string, error) {
	var doc struct {
		Schema     string   `json:"schema"`
		Monitor    []string `json:"monitor"`
		Deployment []string `json:"deployment_grants"`
	}
	if decodeStrict(body, &doc) != nil || doc.Schema != "dill.agent.capabilities.v1" || doc.Monitor == nil || doc.Deployment == nil || len(doc.Monitor)+len(doc.Deployment) > 32 {
		return nil, ErrProtocol
	}
	out := []string{}
	seen := map[string]bool{}
	for i, group := range [][]string{doc.Monitor, doc.Deployment} {
		prefix := "monitor."
		if i == 1 {
			prefix = "deployment."
		}
		for _, v := range group {
			if v == "" || len(v) > 64 || strings.Trim(v, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" || seen[prefix+v] {
				return nil, ErrProtocol
			}
			seen[prefix+v] = true
			out = append(out, prefix+v)
		}
	}
	if !seen["monitor.session"] || !seen["monitor.capabilities"] {
		return nil, ErrProtocol
	}
	return out, nil
}
