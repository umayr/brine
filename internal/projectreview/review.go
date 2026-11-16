// Package projectreview provides dormant, in-memory, one-shot review consent.
// It performs no user authorization, network activity, target attestation, Pkl
// sandboxing, transfer, or deployment. Trusted callers must authorize every call,
// validate every path against receiver rules before review without silently
// excluding files, and supply current authoritative configuration at confirmation
// and consumption. A pin is asserted configuration, not proof of a live host.
// Bound requests and rate-limit at external boundaries; protect browser calls
// against CSRF, do not log sensitive data, and use no-store/redaction for previews.
//
// Restart loses previews and approvals. Consumption precedes submission, so a
// subsequent submission failure can have an unknown outcome. Never retry an
// approval; reconcile and review again. This is not distributed replay protection.
package projectreview

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/umayr/brine/internal/projectsnapshot"
)

var ErrInvalid = errors.New("invalid or unavailable review")
var ErrCapacity = errors.New("review capacity exceeded")
var ErrClosed = errors.New("review store closed")

type TargetKind string

const (
	Local TargetKind = "local"
	SSH   TargetKind = "ssh"
)

// Target is asserted by the trusted caller, not an attestation. SSHKnownHost is
// an OpenSSH SHA256: fingerprint of the verified host key; Endpoint specifies
// the SSH routing destination. Socket is the destination's socket path.
type Target struct {
	Kind                                            TargetKind
	Identity, Socket, Endpoint, SSHKnownHost        string
	SSHUser, SSHHost, SSHIdentityFile, SSHPublicKey string
	SSHPort                                         uint16
}

// Current is the effective, non-secret configuration. Include non-secret
// versions/fingerprints for any execution-affecting secret changes as needed.
type Current struct {
	Stack              string
	Target             Target
	Variables, Options map[string]string
}

// Intent is captured once at Create; callers must not mutate inputs concurrently.
type Intent struct {
	Snapshot projectsnapshot.Snapshot
	Current  Current
}

// File is public metadata only; it cannot expose captured content by serialization.
type File struct {
	Path   string
	Size   int64
	SHA256 [sha256.Size]byte
}
type Preview struct {
	ID         string
	Entrypoint string
	Files      []File
	Current    Current
	Expires    time.Time
}
type entry struct {
	session   string
	intent    Intent
	expires   time.Time
	token     [32]byte
	confirmed bool
	size      int64
}

// Store bounds the number and accounted size of retained intents. Close must be
// called to stop its expiry worker and release references. Expiry is swept in
// the background and checked on every operation; scheduling may delay cleanup.
// Go does not guarantee zeroization or immediate heap reclamation. Accounting is
// a conservative logical budget, not an exact heap limit. In-flight validation
// reserves count and byte capacity. TTL must be at least one second.
type Store struct {
	mu             sync.Mutex
	entries        map[string]*entry
	maxCount       int
	maxBytes, used int64
	reservedCount  int
	reservedBytes  int64
	ttl            time.Duration
	now            func() time.Time
	done           chan struct{}
	stopped        chan struct{}
	closed         bool
}

func New(maxCount int, maxBytes int64, ttl time.Duration) (*Store, error) {
	if maxCount <= 0 || maxBytes <= 0 || ttl < time.Second {
		return nil, ErrInvalid
	}
	s := &Store{entries: make(map[string]*entry), maxCount: maxCount, maxBytes: maxBytes, ttl: ttl, now: time.Now, done: make(chan struct{}), stopped: make(chan struct{})}
	interval := ttl / 2
	if interval > time.Minute {
		interval = time.Minute
	}
	go s.sweep(interval)
	return s, nil
}
func (s *Store) sweep(interval time.Duration) {
	defer close(s.stopped)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.mu.Lock()
			s.purge(s.now())
			s.mu.Unlock()
		case <-s.done:
			return
		}
	}
}

// Close is idempotent, invalidates all approvals and waits for the expiry
// worker, but not in-flight Create validators. Those calls release their own
// reservations when they return and cannot create entries after Close.
func (s *Store) Close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.done)
		clear(s.entries)
		s.used = 0
	}
	s.mu.Unlock()
	<-s.stopped
}
func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	n := make(map[string]string, len(m))
	for k, v := range m {
		n[strings.Clone(k)] = strings.Clone(v)
	}
	return n
}
func copyCurrent(c Current) Current {
	c.Stack = strings.Clone(c.Stack)
	c.Target.Identity = strings.Clone(c.Target.Identity)
	c.Target.Socket = strings.Clone(c.Target.Socket)
	c.Target.Endpoint = strings.Clone(c.Target.Endpoint)
	c.Target.SSHKnownHost = strings.Clone(c.Target.SSHKnownHost)
	c.Target.SSHUser = strings.Clone(c.Target.SSHUser)
	c.Target.SSHHost = strings.Clone(c.Target.SSHHost)
	c.Target.SSHIdentityFile = strings.Clone(c.Target.SSHIdentityFile)
	c.Target.SSHPublicKey = strings.Clone(c.Target.SSHPublicKey)
	c.Variables = copyMap(c.Variables)
	c.Options = copyMap(c.Options)
	return c
}
func clone(in Intent) Intent {
	out := in
	out.Current = copyCurrent(in.Current)
	out.Snapshot.Entrypoint = strings.Clone(in.Snapshot.Entrypoint)
	out.Snapshot.Files = make([]projectsnapshot.File, len(in.Snapshot.Files))
	for i, f := range in.Snapshot.Files {
		out.Snapshot.Files[i] = f
		out.Snapshot.Files[i].Path = strings.Clone(f.Path)
		out.Snapshot.Files[i].Content = append([]byte(nil), f.Content...)
	}
	return out
}
func validCurrent(c Current) bool {
	t := c.Target
	if c.Stack == "" || t.Identity == "" || t.Socket == "" {
		return false
	}
	switch t.Kind {
	case Local:
		return t.Endpoint == "" && t.SSHKnownHost == "" && t.SSHUser == "" && t.SSHHost == "" && t.SSHIdentityFile == "" && t.SSHPublicKey == "" && t.SSHPort == 0
	case SSH:
		if t.Endpoint == "" || !strings.HasPrefix(t.SSHKnownHost, "SHA256:") {
			return false
		}
		p := strings.TrimPrefix(t.SSHKnownHost, "SHA256:")
		b, err := base64.RawStdEncoding.DecodeString(p)
		return err == nil && len(b) == sha256.Size && base64.RawStdEncoding.EncodeToString(b) == p
	default:
		return false
	}
}

// budget checks lengths and counts without hashing or copying. Each item has
// positive overhead so even zero-length collections are bounded by maxBytes.
func budget(in Intent, session string, max int64) (int64, bool) {
	n := int64(512)
	add := func(x int) bool {
		if x < 0 || int64(x) > max-n {
			return false
		}
		n += int64(x)
		return true
	}
	if n > max || !add(len(session)) || !add(len(in.Current.Stack)) || !add(len(in.Current.Target.Kind)) || !add(len(in.Current.Target.Identity)) || !add(len(in.Current.Target.Socket)) || !add(len(in.Current.Target.Endpoint)) || !add(len(in.Current.Target.SSHKnownHost)) || !add(len(in.Current.Target.SSHUser)) || !add(len(in.Current.Target.SSHHost)) || !add(len(in.Current.Target.SSHIdentityFile)) || !add(len(in.Current.Target.SSHPublicKey)) || !add(len(in.Snapshot.Entrypoint)) {
		return 0, false
	}
	if int64(len(in.Snapshot.Files)) > (max-n)/128 {
		return 0, false
	}
	for _, f := range in.Snapshot.Files {
		if !add(128) || !add(len(f.Path)) || !add(len(f.Content)) {
			return 0, false
		}
	}
	for _, m := range []map[string]string{in.Current.Variables, in.Current.Options} {
		if int64(len(m)) > (max-n)/64 {
			return 0, false
		}
		for k, v := range m {
			if !add(64) || !add(len(k)) || !add(len(v)) {
				return 0, false
			}
		}
	}
	return n, true
}
func validate(ctx context.Context, in Intent) error {
	if !validCurrent(in.Current) || len(in.Snapshot.Files) == 0 {
		return ErrInvalid
	}
	found := false
	prev := ""
	for _, f := range in.Snapshot.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := f.Path
		if p == "" || p == "." || strings.Contains(p, "\\") || strings.HasPrefix(p, "/") || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") || (len(p) >= 2 && p[1] == ':') || (prev != "" && p <= prev) {
			return ErrInvalid
		}
		h := sha256.New()
		for off := 0; off < len(f.Content); {
			if err := ctx.Err(); err != nil {
				return err
			}
			end := off + 65536
			if end > len(f.Content) {
				end = len(f.Content)
			}
			h.Write(f.Content[off:end])
			off = end
		}
		if subtle.ConstantTimeCompare(h.Sum(nil), f.SHA256[:]) != 1 {
			return ErrInvalid
		}
		if p == in.Snapshot.Entrypoint {
			found = true
		}
		prev = p
	}
	if !found {
		return ErrInvalid
	}
	return ctx.Err()
}
func (s *Store) purge(now time.Time) {
	for id, e := range s.entries {
		if !now.Before(e.expires) {
			s.used -= e.size
			delete(s.entries, id)
		}
	}
}
func random() ([32]byte, error) { var b [32]byte; _, err := rand.Read(b[:]); return b, err }

// Create retains the exact captured bytes privately. Preview exposes metadata only.
func (s *Store) Create(ctx context.Context, session string, intent Intent) (Preview, error) {
	if err := ctx.Err(); err != nil {
		return Preview{}, err
	}
	if session == "" {
		return Preview{}, ErrInvalid
	}
	n, ok := budget(intent, session, s.maxBytes)
	if !ok {
		return Preview{}, ErrCapacity
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Preview{}, ErrClosed
	}
	s.purge(s.now())
	if len(s.entries)+s.reservedCount >= s.maxCount || n > s.maxBytes-s.used-s.reservedBytes {
		s.mu.Unlock()
		return Preview{}, ErrCapacity
	}
	s.reservedCount++
	s.reservedBytes += n
	s.mu.Unlock()
	reserved := true
	defer func() {
		if reserved {
			s.mu.Lock()
			s.reservedCount--
			s.reservedBytes -= n
			s.mu.Unlock()
		}
	}()
	if err := validate(ctx, intent); err != nil {
		return Preview{}, err
	}
	id, err := random()
	if err != nil {
		return Preview{}, err
	}
	key := hex.EncodeToString(id[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return Preview{}, err
	}
	if s.closed {
		return Preview{}, ErrClosed
	}
	now := s.now()
	s.purge(now)
	if _, ok := s.entries[key]; ok {
		return Preview{}, ErrInvalid
	}
	e := &entry{session: strings.Clone(session), intent: clone(intent), expires: now.Add(s.ttl), size: n}
	s.reservedCount--
	s.reservedBytes -= n
	reserved = false
	s.entries[key] = e
	s.used += n
	p := Preview{ID: key, Entrypoint: strings.Clone(e.intent.Snapshot.Entrypoint), Current: copyCurrent(e.intent.Current), Expires: e.expires, Files: make([]File, len(e.intent.Snapshot.Files))}
	for i, f := range e.intent.Snapshot.Files {
		p.Files[i] = File{Path: strings.Clone(f.Path), Size: int64(len(f.Content)), SHA256: f.SHA256}
	}
	return p, nil
}
func equalMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || v != w {
			return false
		}
	}
	return true
}
func equalCurrent(a, b Current) bool {
	return a.Stack == b.Stack && a.Target == b.Target && equalMap(a.Variables, b.Variables) && equalMap(a.Options, b.Options)
}

// Confirm requires explicit separate confirmation. Current must be freshly
// resolved by the trusted caller, not merely echoed from the preview. An
// observed configuration mismatch permanently revokes the preview.
func (s *Store) Confirm(ctx context.Context, session, id string, current Current) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.closed {
		return "", ErrClosed
	}
	s.purge(s.now())
	e := s.entries[id]
	if e == nil || e.session != session {
		return "", ErrInvalid
	}
	if !equalCurrent(e.intent.Current, current) {
		delete(s.entries, id)
		s.used -= e.size
		return "", ErrInvalid
	}
	if e.confirmed {
		return "", ErrInvalid
	}
	token, err := random()
	if err != nil {
		return "", err
	}
	e.token = token
	e.confirmed = true
	return hex.EncodeToString(token[:]), nil
}

// Consume atomically invalidates approval before returning private captured bytes.
// A verified token with changed configuration revokes the preview; other failed
// checks do not consume it. Caller must submit only the returned snapshot.
func (s *Store) Consume(ctx context.Context, session, id, token string, current Current) (Intent, error) {
	if err := ctx.Err(); err != nil {
		return Intent{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Intent{}, err
	}
	if s.closed {
		return Intent{}, ErrClosed
	}
	s.purge(s.now())
	e := s.entries[id]
	if e == nil || e.session != session || !e.confirmed {
		return Intent{}, ErrInvalid
	}
	if len(token) != 64 {
		return Intent{}, ErrInvalid
	}
	b, err := hex.DecodeString(token)
	if err != nil || len(b) != 32 || subtle.ConstantTimeCompare(e.token[:], b) != 1 {
		return Intent{}, ErrInvalid
	}
	if !equalCurrent(e.intent.Current, current) {
		delete(s.entries, id)
		s.used -= e.size
		return Intent{}, ErrInvalid
	}
	delete(s.entries, id)
	s.used -= e.size
	return clone(e.intent), nil
}
