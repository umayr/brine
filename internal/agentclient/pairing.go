package agentclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

// Pairing is an in-memory, unpersisted manual HTTPS enrollment candidate. It
// deliberately has no exported fields or serialization API. Callers must not
// log the original input. This component is not an authorization boundary or a
// registry; accepting a target requires an independently reviewed, durable
// binding and credential lifecycle.
type Pairing struct {
	pin, token string
	binding    Binding
}

// String and GoString prevent ordinary formatting of a candidate from revealing
// the credential. They do not sanitize the caller's original input.
func (Pairing) String() string               { return "[HTTPS pairing redacted]" }
func (Pairing) GoString() string             { return "[HTTPS pairing redacted]" }
func (Pairing) MarshalJSON() ([]byte, error) { return nil, ErrProtocol }

// ParsePairing accepts exactly the v1 host bundle, with no duplicate or extra
// fields. All failures are deliberately indistinguishable and contain no input.
func ParsePairing(input []byte) (*Pairing, error) {
	if len(input) == 0 || len(input) > 16384 || !utf8.Valid(input) || checkJSON(input) != nil {
		return nil, ErrProtocol
	}
	d := json.NewDecoder(bytes.NewReader(input))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return nil, ErrProtocol
	}
	fields := make(map[string]json.RawMessage)
	allowed := map[string]bool{"version": true, "certificateSha256": true, "token": true, "agentPublicKey": true, "engineId": true, "generation": true, "engineKind": true, "engineSocket": true, "daemonId": true}
	for d.More() {
		k, e := d.Token()
		if e != nil {
			return nil, ErrProtocol
		}
		name, ok := k.(string)
		if !ok || !allowed[name] {
			return nil, ErrProtocol
		}
		if _, exists := fields[name]; exists {
			return nil, ErrProtocol
		}
		var v json.RawMessage
		if d.Decode(&v) != nil {
			return nil, ErrProtocol
		}
		fields[name] = v
	}
	if t, err = d.Token(); err != nil || t != json.Delim('}') || len(fields) != len(allowed) {
		return nil, ErrProtocol
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrProtocol
	}
	var v struct {
		Version     uint64 `json:"version"`
		Certificate string `json:"certificateSha256"`
		Token       string `json:"token"`
		Agent       string `json:"agentPublicKey"`
		EngineID    string `json:"engineId"`
		Generation  uint64 `json:"generation"`
		Kind        string `json:"engineKind"`
		Socket      string `json:"engineSocket"`
		DaemonID    string `json:"daemonId"`
	}
	if json.Unmarshal(input, &v) != nil || v.Version != 1 || v.Kind != "docker" {
		return nil, ErrProtocol
	}
	key, err := hex.DecodeString(v.Agent)
	if err != nil || len(key) != ed25519.PublicKeySize || hex.EncodeToString(key) != v.Agent {
		return nil, ErrProtocol
	}
	p := &Pairing{pin: v.Certificate, token: v.Token, binding: Binding{AgentPublicKey: key, EngineID: v.EngineID, Generation: v.Generation, EngineKind: v.Kind, EngineSocket: v.Socket, DaemonID: v.DaemonID}}
	// Reuse the transport's exact binding, pin and credential validation without
	// making a connection. The real endpoint is validated separately by Open.
	c, err := NewHTTPS(context.Background(), "127.0.0.1:1", p.pin, p.token, p.binding)
	if err != nil {
		return nil, ErrProtocol
	}
	_ = c.Close()
	return p, nil
}

// Open authenticates a fresh signed session before returning a read client and
// its session epoch. It makes no persistence or enrollment decision.
// The caller owns the client.
func (p *Pairing) Open(lifetime, request context.Context, address string) (*Client, string, error) {
	if p == nil || request == nil {
		return nil, "", ErrProtocol
	}
	c, err := NewHTTPS(lifetime, address, p.pin, p.token, p.binding)
	if err != nil {
		return nil, "", ErrProtocol
	}
	epoch, err := c.Session(request)
	if err != nil {
		_ = c.Close()
		return nil, "", ErrProtocol
	}
	return c, epoch, nil
}
