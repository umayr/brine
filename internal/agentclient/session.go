// Package agentclient contains the transport-independent authentication primitives for
// the proposed read-only agent protocol. SSH forwarding and enrollment are external.
package agentclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const version = "brine.agent.read/1"

var ErrProtocol = errors.New("agent protocol error")

// Binding must be explicitly enrolled out of band, independently of SSH host keys.
type Binding struct {
	AgentPublicKey                     ed25519.PublicKey
	EngineID                           string
	Generation                         uint64
	EngineKind, EngineSocket, DaemonID string
}
type Proof struct {
	AgentID        string `json:"agentId"`
	AgentPublicKey string `json:"agentPublicKey"`
	EngineID       string `json:"engineId"`
	Generation     uint64 `json:"generation"`
	EngineKind     string `json:"engineKind"`
	EngineSocket   string `json:"engineSocket"`
	DaemonID       string `json:"daemonId"`
	Epoch          string `json:"epoch"`
	Nonce          string `json:"nonce"`
	Signature      string `json:"signature"`
}

func binaryText(s string, n int) ([]byte, error) {
	b, e := base64.RawURLEncoding.DecodeString(s)
	if e != nil || len(b) != n || base64.RawURLEncoding.EncodeToString(b) != s {
		return nil, ErrProtocol
	}
	return b, nil
}
func lp(b *bytes.Buffer, s string) error {
	if !utf8.ValidString(s) || len(s) > 4096 {
		return ErrProtocol
	}
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(s)))
	b.Write(n[:])
	b.WriteString(s)
	return nil
}
func u64(b *bytes.Buffer, n uint64) {
	var x [8]byte
	binary.BigEndian.PutUint64(x[:], n)
	b.Write(x[:])
}

// SessionBytes returns the exact protocol signing input, excluding the signature.
func SessionBytes(p Proof) ([]byte, error) {
	if _, e := binaryText(p.Nonce, 32); e != nil {
		return nil, e
	}
	if _, e := binaryText(p.Epoch, 32); e != nil {
		return nil, e
	}
	if _, e := binaryText(p.AgentPublicKey, 32); e != nil {
		return nil, e
	}
	var b bytes.Buffer
	b.WriteString("brine-agent-session/1\x00")
	for _, s := range []string{p.Nonce, p.AgentID, p.AgentPublicKey, p.EngineID} {
		if e := lp(&b, s); e != nil {
			return nil, e
		}
	}
	u64(&b, p.Generation)
	for _, s := range []string{p.EngineKind, p.EngineSocket, p.DaemonID, p.Epoch} {
		if e := lp(&b, s); e != nil {
			return nil, e
		}
	}
	return b.Bytes(), nil
}
func validBinding(b Binding) bool {
	return len(b.AgentPublicKey) == 32 && validUUID(b.EngineID) && b.Generation > 0 && (b.EngineKind == "docker" || b.EngineKind == "podman") && validSocket(b.EngineSocket) && b.DaemonID != "" && stringsOK(b.DaemonID)
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
func fullID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
func validSocket(s string) bool {
	if !strings.HasPrefix(s, "unix:///") {
		return false
	}
	p := strings.TrimPrefix(s, "unix://")
	return stringsOK(p) && !strings.ContainsAny(p, "\x00?#") && path.IsAbs(p) && path.Clean(p) == p && p != "/"
}

// VerifyProof checks the signed nonce and every independently pinned engine attribute.
func VerifyProof(b Binding, p Proof, nonce string) error {
	if !validBinding(b) {
		return ErrProtocol
	}
	key, e := binaryText(p.AgentPublicKey, 32)
	if e != nil {
		return ErrProtocol
	}
	sig, e := binaryText(p.Signature, 64)
	if e != nil {
		return ErrProtocol
	}
	h := sha256.Sum256(key)
	if !bytes.Equal(key, b.AgentPublicKey) || p.AgentID != hex.EncodeToString(h[:]) || p.Nonce != nonce || p.EngineID != b.EngineID || p.Generation != b.Generation || p.EngineKind != b.EngineKind || p.EngineSocket != b.EngineSocket || p.DaemonID != b.DaemonID {
		return ErrProtocol
	}
	msg, e := SessionBytes(p)
	if e != nil || !ed25519.Verify(key, msg, sig) {
		return ErrProtocol
	}
	return nil
}

// Client owns exactly one HTTP/1 connection for its lifetime. The caller must
// cancel the forward lifetime context immediately if its pinned SSH tunnel fails.
// The client never performs enrollment, SSH authentication, or user authorization.
type Client struct {
	binding      Binding
	http         *http.Client
	mu           sync.Mutex
	sessionMu    sync.Mutex
	epoch        string
	epochCtx     context.Context
	epochCancel  context.CancelFunc
	revealKey    ed25519.PublicKey
	streams      chan struct{}
	used         map[string]uint64
	forward      context.Context
	closeForward context.CancelFunc
	dialed       bool
	dead         bool
}

// New requires the lifetime of the externally owned pinned SSH forward. A lost
// connection permanently closes this client: reconnect with a new client and proof.
func New(forward context.Context, socket string, b Binding) (*Client, error) {
	if forward == nil || forward.Err() != nil || !validSocket("unix://"+socket) || !validBinding(b) {
		return nil, ErrProtocol
	}
	b.AgentPublicKey = append(ed25519.PublicKey(nil), b.AgentPublicKey...)
	forward, closeForward := context.WithCancel(forward)
	c := &Client{binding: b, forward: forward, closeForward: closeForward, streams: make(chan struct{}, 2), used: make(map[string]uint64)}
	t := &http.Transport{Proxy: nil, MaxConnsPerHost: 1, MaxIdleConnsPerHost: 1, ForceAttemptHTTP2: false}
	t.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		c.mu.Lock()
		if c.dead || c.dialed || forward.Err() != nil {
			c.mu.Unlock()
			return nil, ErrProtocol
		}
		c.dialed = true
		c.mu.Unlock()
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if err != nil {
			c.fail()
			return nil, ErrProtocol
		}
		return &ownedConn{Conn: conn, fail: c.fail}, nil
	}
	c.http = &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	context.AfterFunc(forward, func() { c.fail(); t.CloseIdleConnections() })
	return c, nil
}

type ownedConn struct {
	net.Conn
	fail func()
}

func (c *ownedConn) Close() error { err := c.Conn.Close(); c.fail(); return err }
func (c *Client) fail() {
	c.mu.Lock()
	c.dead = true
	c.epoch = ""
	cancel := c.epochCancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Close invalidates the proof, cancels active calls and permanently closes this client.
func (c *Client) Close() error { c.fail(); c.closeForward(); c.http.CloseIdleConnections(); return nil }

// NewWithRevealKey pins the authorized upstream reveal verification key.
func NewWithRevealKey(forward context.Context, socket string, b Binding, revealKey ed25519.PublicKey) (*Client, error) {
	if len(revealKey) != ed25519.PublicKeySize {
		return nil, ErrProtocol
	}
	c, e := New(forward, socket, b)
	if e != nil {
		return nil, e
	}
	c.revealKey = append(ed25519.PublicKey(nil), revealKey...)
	return c, nil
}

// Session authenticates the agent afresh with a random challenge. The returned
// epoch is not proof of a user session and must not authorize reveal by itself.
func (c *Client) Session(ctx context.Context) (string, error) {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c.mu.Lock()
	if c.dead || c.forward.Err() != nil {
		c.mu.Unlock()
		return "", ErrProtocol
	}
	oldCancel := c.epochCancel
	c.epoch = ""
	c.mu.Unlock()
	if oldCancel != nil {
		oldCancel()
	}
	stop := context.AfterFunc(c.forward, cancel)
	defer stop()
	nonce := make([]byte, 32)
	id := make([]byte, 16)
	if _, e := rand.Read(nonce); e != nil {
		return "", ErrProtocol
	}
	if _, e := rand.Read(id); e != nil {
		return "", ErrProtocol
	}
	ns := base64.RawURLEncoding.EncodeToString(nonce)
	rid := hex.EncodeToString(id)
	body, _ := json.Marshal(struct {
		Nonce string `json:"nonce"`
	}{ns})
	req, e := http.NewRequestWithContext(ctx, "POST", "http://unix/v1/session", bytes.NewReader(body))
	if e != nil {
		return "", ErrProtocol
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cache-Control", "no-store")
	req.Header.Set("X-Brine-Binding", fmt.Sprint(c.binding.Generation))
	req.Header.Set("X-Request-Id", rid)
	resp, e := c.http.Do(req)
	if e != nil {
		c.fail()
		return "", ErrProtocol
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.fail()
		return "", ErrProtocol
	}
	if resp.Header.Get("Content-Type") != "application/json" {
		return "", ErrProtocol
	}
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 512*1024+1))
	if e != nil || len(raw) > 512*1024 || !utf8.Valid(raw) || checkJSON(raw) != nil {
		return "", ErrProtocol
	}
	var envelope struct {
		Version   string `json:"version"`
		RequestID string `json:"requestId"`
		Data      Proof  `json:"data"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if requireFields(raw, reflect.TypeOf(envelope)) != nil || d.Decode(&envelope) != nil || envelope.Version != version || envelope.RequestID != rid {
		return "", ErrProtocol
	}
	if VerifyProof(c.binding, envelope.Data, ns) != nil {
		return "", ErrProtocol
	}
	c.mu.Lock()
	if c.dead || c.forward.Err() != nil {
		c.mu.Unlock()
		return "", ErrProtocol
	}
	c.epoch = envelope.Data.Epoch
	c.epochCtx, c.epochCancel = context.WithCancel(context.Background())
	c.mu.Unlock()
	return envelope.Data.Epoch, nil
}

// checkJSON rejects duplicate object names at every nesting level, including
// equivalent escaped spellings, and ensures there is exactly one JSON value.
func checkJSON(raw []byte) error {
	if !validSurrogates(raw) {
		return ErrProtocol
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if walk(d) != nil {
		return ErrProtocol
	}
	if _, e := d.Token(); e != io.EOF {
		return ErrProtocol
	}
	return nil
}
func validSurrogates(b []byte) bool {
	in := false
	for i := 0; i < len(b); i++ {
		if b[i] == '"' {
			in = !in
			continue
		}
		if !in || b[i] != '\\' {
			continue
		}
		i++
		if i >= len(b) {
			return false
		}
		if b[i] != 'u' {
			continue
		}
		if i+4 >= len(b) {
			return false
		}
		v, err := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if v >= 0xdc00 && v <= 0xdfff {
			return false
		}
		if v >= 0xd800 && v <= 0xdbff {
			if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
				return false
			}
			w, e := strconv.ParseUint(string(b[i+3:i+7]), 16, 16)
			if e != nil || w < 0xdc00 || w > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
func walk(d *json.Decoder) error {
	t, e := d.Token()
	if e != nil {
		return e
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return e
			}
			s, ok := k.(string)
			if !ok || seen[s] {
				return ErrProtocol
			}
			seen[s] = true
			if e = walk(d); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return ErrProtocol
		}
	case '[':
		for d.More() {
			if e := walk(d); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return ErrProtocol
		}
	default:
		return ErrProtocol
	}
	return nil
}
