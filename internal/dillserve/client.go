// Package dillserve provides a dormant, read-only client for the v1 Unix-socket
// service. It does not authenticate the service or authorize deployment.
package dillserve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

const maxResponse = 1 << 20

// Each HTTP request gets 15 seconds: the service's 10-second status deadline
// plus 5 seconds for connection and response transport. An earlier caller
// deadline or cancellation still wins. Status performs health and status as
// separate requests; callers can bound the entire operation with their context.
const requestTimeout = 15 * time.Second

var ErrUnavailable = errors.New("dill-serve unavailable or invalid response")

// Target is explicitly supplied by the caller. Stack and Engine are expected
// status values, not authenticated identities or proof of engine provenance.
type Target struct{ Socket, Stack, Engine string }
type Health struct {
	APIVersion   string
	Capabilities []string
}
type Status struct {
	Stack, Engine string
	Containers    []json.RawMessage
}

type Client struct {
	target    Target
	http      *http.Client
	transport *http.Transport
}

// NewLocal never starts a process, consults environment configuration, or falls
// back to a CLI or engine. The caller is responsible for socket access control.
func NewLocal(t Target) (*Client, error) {
	if !filepath.IsAbs(t.Socket) || strings.IndexByte(t.Socket, 0) >= 0 || t.Stack == "" || t.Engine == "" {
		return nil, ErrUnavailable
	}
	tr := &http.Transport{DisableKeepAlives: true, MaxResponseHeaderBytes: 16 << 10, Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", t.Socket)
	}}
	return &Client{target: t, transport: tr, http: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Close() { c.transport.CloseIdleConnections() }

// object rejects duplicate keys and trailing values, including null objects.
func object(data []byte, keys ...string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	tok, e := d.Token()
	if e != nil || tok != json.Delim('{') {
		return nil, ErrUnavailable
	}
	m := make(map[string]json.RawMessage)
	for d.More() {
		tok, e = d.Token()
		if e != nil {
			return nil, ErrUnavailable
		}
		k, ok := tok.(string)
		if !ok {
			return nil, ErrUnavailable
		}
		if _, exists := m[k]; exists {
			return nil, ErrUnavailable
		}
		var v json.RawMessage
		if d.Decode(&v) != nil {
			return nil, ErrUnavailable
		}
		m[k] = v
	}
	if _, e = d.Token(); e != nil {
		return nil, ErrUnavailable
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, ErrUnavailable
	}
	for _, k := range keys {
		v, ok := m[k]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return nil, ErrUnavailable
		}
	}
	return m, nil
}
func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	return c.getLimit(ctx, path, maxResponse)
}
func (c *Client) getLimit(ctx context.Context, path string, limit int64) ([]byte, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, "http://dillserve"+path, nil)
	if e != nil {
		return nil, ErrUnavailable
	}
	resp, e := c.http.Do(req)
	if e != nil {
		return nil, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrUnavailable
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if e != nil || int64(len(b)) > limit {
		return nil, ErrUnavailable
	}
	return b, nil
}
func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	b, e := c.get(ctx, "/v1/health")
	if e != nil {
		return h, e
	}
	m, e := object(b, "api_version", "capabilities")
	if e != nil {
		return h, e
	}
	if json.Unmarshal(m["api_version"], &h.APIVersion) != nil || h.APIVersion != "v1" || json.Unmarshal(m["capabilities"], &h.Capabilities) != nil {
		return Health{}, ErrUnavailable
	}
	seen := map[string]bool{}
	for _, v := range h.Capabilities {
		if v == "" || seen[v] {
			return Health{}, ErrUnavailable
		}
		seen[v] = true
	}
	if !seen["health"] || !seen["status"] {
		return Health{}, ErrUnavailable
	}
	return h, nil
}

// Status negotiates capabilities on each call; it never invokes an unadvertised method.
func (c *Client) Status(ctx context.Context) (Status, error) {
	if _, e := c.Health(ctx); e != nil {
		return Status{}, e
	}
	return c.status(ctx)
}

func (c *Client) status(ctx context.Context) (Status, error) {
	var s Status
	b, e := c.get(ctx, "/v1/status")
	if e != nil {
		return s, e
	}
	m, e := object(b, "stack", "engine", "containers")
	if e != nil {
		return s, e
	}
	if json.Unmarshal(m["stack"], &s.Stack) != nil || json.Unmarshal(m["engine"], &s.Engine) != nil || json.Unmarshal(m["containers"], &s.Containers) != nil || s.Containers == nil || s.Stack != c.target.Stack || s.Engine != c.target.Engine {
		return Status{}, fmt.Errorf("%w: status target mismatch or malformed status", ErrUnavailable)
	}
	return s, nil
}
