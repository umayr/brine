package agentclient

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
)

// OpenDeploymentCurrent attaches immutable custody-generation checks to every
// request after the fresh monitor proof. No live client can swap credentials.
func (p *Pairing) OpenDeploymentCurrent(lifetime, request context.Context, address, credential string, current func(context.Context) bool) (*Client, string, error) {
	if request == nil || current == nil || !current(request) {
		return nil, "", ErrProtocol
	}
	c, epoch, e := p.OpenDeployment(lifetime, request, address, credential)
	if e != nil {
		return nil, "", e
	}
	c.requestGate = func(req *http.Request) bool {
		return req.URL.Opaque == "" && req.URL.Scheme == "https" && req.URL.Host == strings.TrimPrefix(c.baseURL, "https://") && req.URL.User == nil && req.URL.RawPath == "" && req.URL.RawQuery == "" && !req.URL.ForceQuery && req.URL.Fragment == "" && req.Context().Err() == nil && current(req.Context())
	}
	return c, epoch, nil
}
func validDeployCredential(monitor, deploy string) bool {
	b, e := base64.RawURLEncoding.Strict().DecodeString(deploy)
	return e == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == deploy && subtle.ConstantTimeCompare([]byte(monitor), []byte(deploy)) != 1
}

// WithDeploymentCredential constructs a fresh pinned client with distinct,
// immutable credentials before session proof. It grants no operation by itself.
func NewHTTPSWithDeploymentCredential(ctx context.Context, address, pin, monitor, deploy string, b Binding) (*Client, error) {
	if !validDeployCredential(monitor, deploy) {
		return nil, ErrProtocol
	}
	c, e := NewHTTPS(ctx, address, pin, monitor, b)
	if e != nil {
		return nil, e
	}
	c.deployBearer = deploy
	return c, nil
}
func deploymentRoute(r *http.Request) bool {
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" {
		return false
	}
	path := r.URL.Path
	if r.Method == http.MethodPost {
		switch path {
		case "/v1/deploy/checked-apply", "/v1/snapshots", "/v1/deploy/preview", "/v1/deploy/apply", "/v1/deploy/journal-apply", "/v1/deploy/fence", "/v1/deploy/remove-preview", "/v1/deploy/remove", "/v1/deploy/start-preview", "/v1/deploy/start", "/v1/deploy/stop-preview", "/v1/deploy/stop", "/v1/deploy/restart-preview", "/v1/deploy/restart":
			return true
		}
	}
	if r.Method == http.MethodGet {
		if path == "/v1/deploy/journal" {
			return true
		}
		for _, prefix := range []string{"/v1/snapshots/", "/v1/deploy/operations/"} {
			if id, ok := strings.CutPrefix(path, prefix); ok && canonicalHex(id, 16) {
				return true
			}
		}
	}
	return false
}
func requiresDeploymentCredential(r *http.Request) bool {
	return r.URL.Path == "/v1/snapshots" || strings.HasPrefix(r.URL.Path, "/v1/snapshots/") || strings.HasPrefix(r.URL.Path, "/v1/deploy/")
}
