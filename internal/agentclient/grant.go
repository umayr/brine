package agentclient

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
)

// Grant is issued by an authorized upstream signer after interactive confirmation.
// This package deliberately does not mint grants or establish user sessions.
type Grant struct {
	AgentID     string `json:"agentId"`
	EngineID    string `json:"engineId"`
	Generation  uint64 `json:"generation"`
	Epoch       string `json:"epoch"`
	ContainerID string `json:"containerId"`
	SessionID   string `json:"sessionId"`
	Nonce       string `json:"nonce"`
	RequestID   string `json:"requestId"`
	IssuedAt    uint64 `json:"issuedAt"`
	ExpiresAt   uint64 `json:"expiresAt"`
	Scope       string `json:"scope"`
	Interactive bool   `json:"interactive"`
	Signature   string `json:"signature"`
}

// NewRequestID generates a unique random request ID for inclusion before signing.
func NewRequestID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func validRequestID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// GrantBytes returns the prescribed signature input. It does not check time or
// replay; those checks must also be enforced by the agent.
func GrantBytes(g Grant) ([]byte, error) {
	if _, e := binaryText(g.Epoch, 32); e != nil {
		return nil, e
	}
	if _, e := binaryText(g.SessionID, 16); e != nil {
		return nil, e
	}
	if _, e := binaryText(g.Nonce, 32); e != nil {
		return nil, e
	}
	if !validRequestID(g.RequestID) {
		return nil, ErrProtocol
	}
	if g.Scope != "env.values" || !g.Interactive || !fullID(g.ContainerID) || g.AgentID == "" || !validUUID(g.EngineID) || g.Generation == 0 || g.ExpiresAt < g.IssuedAt || g.ExpiresAt-g.IssuedAt > 30 {
		return nil, ErrProtocol
	}
	var b bytes.Buffer
	b.WriteString("brine-agent-reveal/2\x00")
	for _, s := range []string{g.AgentID, g.EngineID} {
		if e := lp(&b, s); e != nil {
			return nil, e
		}
	}
	u64(&b, g.Generation)
	for _, s := range []string{g.Epoch, g.ContainerID, g.SessionID, g.Nonce, g.RequestID} {
		if e := lp(&b, s); e != nil {
			return nil, e
		}
	}
	u64(&b, g.IssuedAt)
	u64(&b, g.ExpiresAt)
	if e := lp(&b, g.Scope); e != nil {
		return nil, e
	}
	b.WriteByte(1)
	return b.Bytes(), nil
}

// VerifyGrant verifies the upstream key and exact target tuple; it is not replay protection.
func VerifyGrant(key ed25519.PublicKey, g Grant, agentID, engineID string, generation uint64, epoch, containerID string, now uint64) error {
	if len(key) != 32 || g.AgentID != agentID || g.EngineID != engineID || g.Generation != generation || g.Epoch != epoch || g.ContainerID != containerID {
		return ErrProtocol
	}
	if (g.IssuedAt > now && g.IssuedAt-now > 5) || (now > g.ExpiresAt && now-g.ExpiresAt > 5) {
		return ErrProtocol
	}
	sig, e := binaryText(g.Signature, 64)
	if e != nil {
		return ErrProtocol
	}
	m, e := GrantBytes(g)
	if e != nil || !ed25519.Verify(key, m, sig) {
		return ErrProtocol
	}
	return nil
}
