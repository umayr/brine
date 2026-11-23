package agentmetrics

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"unicode/utf8"
)

const aggregateCapability = "brine.agent.metrics.aggregate/1"

// ErrUnavailable means the signed, well-formed discovery omitted the supported capability.
var ErrUnavailable = errors.New("metrics capability unavailable")

// DiscoveryAttempt is an offline, single-completion slot. The caller owns HTTP
// framing, request deadlines, unique IDs, membership, and invalidation when a
// session or target changes. Never copy it after use.
type DiscoveryAttempt struct {
	mu              sync.Mutex
	ctx             Context
	target, session string
}

// NewDiscoveryAttempt requires locally assigned, nonempty target and session
// identities, explicit intent, caller authority, and a verified active session.
// Capability authorization is NOT inferred from the monitor credential.
func NewDiscoveryAttempt(c Context, target, session string) *DiscoveryAttempt {
	if target == "" || session == "" || c.Route != DiscoveryRoute || !c.OptIn || !c.CallerAuthorized || !c.SessionVerified || !c.AttemptActive || len(c.PublicKey) != 32 {
		return &DiscoveryAttempt{}
	}
	c.PublicKey = append([]byte(nil), c.PublicKey...)
	return &DiscoveryAttempt{ctx: c, target: target, session: session}
}

func (a *DiscoveryAttempt) Invalidate() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ctx = Context{}
	a.target = ""
	a.session = ""
}

// DiscoveryProof is an opaque, non-portable receipt, not an authorization grant.
// Its lifecycle and replacement on newer discovery are caller responsibilities.
type DiscoveryProof struct {
	target, session, agent, engine, epoch, key string
	generation                                 uint64
	valid                                      bool
}

// Matches requires current local authority and current discovery selection. The
// caller must invalidate on aging, revocation, replacement, suspend or rebind;
// this method does not measure time or establish session membership.
func (p DiscoveryProof) Matches(c Context, target, session string, current bool) bool {
	return p.valid && current && c.OptIn && c.CallerAuthorized && c.SessionVerified && c.AttemptActive && target == p.target && session == p.session && c.AgentID == p.agent && c.EngineID == p.engine && c.Epoch == p.epoch && c.Generation == p.generation && string(c.PublicKey) == p.key
}

// Consume accepts a signed, correlated discovery success at most once.
func (a *DiscoveryAttempt) Consume(status int, signatures []string, body []byte) (DiscoveryProof, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, target, session := a.ctx, a.target, a.session
	a.ctx = Context{}
	a.target = ""
	a.session = ""
	if !c.AttemptActive || status != 200 {
		return DiscoveryProof{}, ErrProof
	}
	raw, err := verifyProof(c, signatures, body)
	if err != nil {
		return DiscoveryProof{}, err
	}
	if err = decodeDiscovery(raw, c.RequestID); err != nil {
		return DiscoveryProof{}, err
	}
	return DiscoveryProof{target: target, session: session, agent: c.AgentID, engine: c.EngineID, epoch: c.Epoch, key: string(c.PublicKey), generation: c.Generation, valid: true}, nil
}

func decodeDiscovery(body []byte, id string) error {
	bad := errors.New("invalid discovery envelope")
	if len(body) > maxBody || !utf8.Valid(body) || id == "" {
		return bad
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	v, err := readValue(d, 1)
	if err != nil {
		return bad
	}
	if _, err = d.Token(); err != io.EOF {
		return bad
	}
	root, ok := v.(map[string]any)
	if !ok || len(root) != 3 || root["version"] != "brine.agent.read/1" || root["requestId"] != id {
		return bad
	}
	data, ok := root["data"].(map[string]any)
	if !ok || len(data) != 1 {
		return bad
	}
	caps, ok := data["capabilities"].([]any)
	if !ok || len(caps) > 8 {
		return bad
	}
	seen := map[string]bool{}
	found := false
	for _, v := range caps {
		s, ok := v.(string)
		if !ok || len(s) < 1 || len(s) > 64 || seen[s] {
			return bad
		}
		for _, c := range s {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '/' || c == '-') {
				return bad
			}
		}
		seen[s] = true
		found = found || s == aggregateCapability
	}
	if !found {
		return ErrUnavailable
	}
	return nil
}
