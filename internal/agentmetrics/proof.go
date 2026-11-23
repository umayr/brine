// Package agentmetrics contains inert offline building blocks, not a metrics client.
// Possession of a monitor token is not authorization. The caller must establish
// authorization, enrollment, session, discovery and a fresh active request slot.
// HTTP bounds, framing, status, route dispatch, receipt time, generation lifetime,
// replay/history and upstream authorization are caller or future responsibilities.
// Authenticated bytes are not displayable until a strict correlated parser accepts them.
package agentmetrics

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"unicode/utf8"
)

const (
	DiscoveryRoute = "/v1/monitor/capabilities"
	SampleRoute    = "/v1/monitor/metrics"
	maxBody        = 4096
)

// Context must be derived solely from the local verified enrollment and active
// request, never from reply fields. All flags are explicit fail-closed assertions.
// Freshness and single-use of RequestID and slot invalidation are caller duties.
type Context struct {
	OptIn, CallerAuthorized, CapabilityAuthorized, SessionVerified, AttemptActive bool
	DiscoveryVerified                                                             bool // required for sampling, for this same session
	Method, Route, RequestID, AgentID, EngineID, Epoch                            string
	Generation                                                                    uint64
	PublicKey                                                                     ed25519.PublicKey
}

var ErrProof = errors.New("invalid monitor proof")

func canonicalBytes(s string, n int) bool {
	if len(s) != base64.RawURLEncoding.EncodedLen(n) {
		return false
	}
	b, e := base64.RawURLEncoding.DecodeString(s)
	return e == nil && len(b) == n && base64.RawURLEncoding.EncodeToString(b) == s
}
func lowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// verifyProof authenticates exactly one detached signature over the bounded body.
// It returns a copy of the bytes only; it does not parse JSON, check the echo,
// or confer permission to present the data. Signatures must be individual raw
// header field values, not a joined or pre-deduplicated collection.
func verifyProof(c Context, signatures []string, body []byte) ([]byte, error) {
	if !c.OptIn || !c.CallerAuthorized || !c.CapabilityAuthorized || !c.SessionVerified || !c.AttemptActive || c.Method != "POST" || (c.Route != DiscoveryRoute && c.Route != SampleRoute) || (c.Route == SampleRoute && !c.DiscoveryVerified) || c.Generation == 0 || !lowerHex(c.RequestID, 32) || !canonicalBytes(c.Epoch, 32) || len(c.PublicKey) != ed25519.PublicKeySize || len(body) > maxBody || !utf8.Valid(body) || len(signatures) != 1 || !canonicalBytes(signatures[0], ed25519.SignatureSize) {
		return nil, ErrProof
	}
	keyID := sha256.Sum256(c.PublicKey)
	if c.AgentID != hex.EncodeToString(keyID[:]) || len(c.EngineID) != 36 || !validUUID(c.EngineID) {
		return nil, ErrProof
	}
	// Snapshot before hashing and returning: callers must not mutate inputs concurrently.
	b := append([]byte(nil), body...)
	hash := sha256.Sum256(b)
	pre := []byte("brine-agent-monitor-response/1\x00")
	for _, s := range []string{c.Method, c.Route, c.RequestID, c.AgentID, c.EngineID} {
		pre = appendString(pre, s)
	}
	pre = binary.BigEndian.AppendUint64(pre, c.Generation)
	pre = appendString(pre, c.Epoch)
	pre = append(pre, hash[:]...)
	sig, _ := base64.RawURLEncoding.DecodeString(signatures[0])
	if !ed25519.Verify(c.PublicKey, pre, sig) {
		return nil, ErrProof
	}
	return b, nil
}
func appendString(b []byte, s string) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
	return append(b, s...)
}
func validUUID(s string) bool {
	for i := 0; i < len(s); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if s[i] != '-' {
				return false
			}
			continue
		}
		if !(s[i] >= '0' && s[i] <= '9' || s[i] >= 'a' && s[i] <= 'f') {
			return false
		}
	}
	return true
}
