package secretbroker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/umayr/brine/internal/secrets"
)

// ReviewIdentity binds preparation to a cookie-session digest, source capture,
// target identity, operator policy and an immutable binding revision. Digests
// are identifiers, never authority; Authorize must recheck actual permission.
type ReviewIdentity struct {
	Scope                                                     Scope
	SourceDigest, TargetDigest, PolicyRevision, SessionDigest string
	BindingRevision                                           int64
}

type Revision struct{ Name, Connection, ProviderRevision string }
type Preparation struct {
	ID        string
	Identity  ReviewIdentity
	ExpiresAt time.Time
	Revisions []Revision
}
type Acquirer interface {
	Acquire(context.Context, secrets.Selector) (secrets.Value, error)
}
type Authorize func(context.Context, ReviewIdentity, []Binding) error

type plaintext struct {
	name  string
	value func() []byte
}
type prepared struct {
	metadata Preparation
	values   []plaintext
	size     int
}

// Delivery is intentionally not serializable or printable. Only a trusted
// transport calls Each to stream private bytes to the reviewed target.
type Delivery struct{ values []plaintext }

func (Delivery) Format(w fmt.State, _ rune)   { redact(w, "Delivery") }
func (Delivery) MarshalJSON() ([]byte, error) { return nil, secrets.ErrValueSerialization }
func (d *Delivery) Each(fn func(name string, value []byte) error) error {
	if d == nil || fn == nil {
		return ErrUnavailable
	}
	for _, v := range d.values {
		if err := fn(v.name, v.value()); err != nil {
			return ErrUnavailable
		}
	}
	return nil
}

// Close is best-effort lifetime reduction, not a memory erasure guarantee.
func (d *Delivery) Close() {
	if d != nil {
		for _, v := range d.values {
			clear(v.value())
		}
		d.values = nil
	}
}

// Manager holds no durable plaintext. Restart loses prepared reviews, never
// reconstructs them from a receipt. The caller must invalidate associated UI
// approvals. No goroutines or network access survive the caller context.
type Manager struct {
	policy    sync.RWMutex
	mu        sync.Mutex
	closed    bool
	store     *Store
	acquire   Acquirer
	authorize Authorize
	pending   map[string]*prepared
	reserved  int
	bytes     int
	now       func() time.Time
	shutdown  context.Context
	stop      context.CancelFunc
}

const (
	maxPending       = 32
	maxPreparedBytes = 8 << 20
	maxBundleBytes   = 1 << 20
	preparationTTL   = 5 * time.Minute
)

func NewManager(store *Store, acquire Acquirer, authorize Authorize) (*Manager, error) {
	if store == nil || store.Identity() == "" || acquire == nil || authorize == nil {
		return nil, ErrInvalidInput
	}
	shutdown, stop := context.WithCancel(context.Background())
	return &Manager{shutdown: shutdown, stop: stop, store: store, acquire: acquire, authorize: authorize, pending: map[string]*prepared{}, now: time.Now}, nil
}
func validDigest(s string) bool {
	if !strings.HasPrefix(s, "sha256:") || len(s) != 71 {
		return false
	}
	b, err := hex.DecodeString(s[7:])
	return err == nil && len(b) == 32 && strings.ToLower(s) == s
}
func validReview(i ReviewIdentity) bool {
	return validScope(i.Scope) && i.BindingRevision >= 0 && validDigest(i.SourceDigest) && validDigest(i.TargetDigest) && validDigest(i.PolicyRevision) && validDigest(i.SessionDigest)
}
func (ReviewIdentity) Format(w fmt.State, _ rune) { redact(w, "ReviewIdentity") }
func (Preparation) Format(w fmt.State, _ rune)    { redact(w, "Preparation") }
func clonePreparation(p Preparation) Preparation {
	p.Revisions = append([]Revision(nil), p.Revisions...)
	return p
}
func erase(p *prepared) {
	for _, v := range p.values {
		clear(v.value())
	}
	p.values = nil
}
func (m *Manager) purgeLocked() {
	for id, p := range m.pending {
		if !m.now().Before(p.metadata.ExpiresAt) {
			m.bytes -= p.size
			erase(p)
			delete(m.pending, id)
		}
	}
}

// Prepare requires an exact logical requirement set, which the caller obtains
// from the captured source, not from a browser value field. Every binding is
// authorized both before resolution and after the immutable snapshot is frozen.
func (m *Manager) Prepare(ctx context.Context, i ReviewIdentity, requirements []string) (Preparation, error) {
	if m == nil || ctx == nil || !validReview(i) || len(requirements) == 0 || len(requirements) > MaxBindingsPerRevision {
		return Preparation{}, ErrInvalidInput
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	unlink := context.AfterFunc(m.shutdown, cancel)
	defer unlink()
	names := append([]string(nil), requirements...)
	sort.Strings(names)
	for n, name := range names {
		if !token(name, maxNameLength, "_.-") || n > 0 && name == names[n-1] {
			return Preparation{}, ErrInvalidInput
		}
	}
	m.mu.Lock()
	m.purgeLocked()
	if m.closed {
		m.mu.Unlock()
		return Preparation{}, ErrUnavailable
	}
	if len(m.pending)+m.reserved >= maxPending {
		m.mu.Unlock()
		return Preparation{}, ErrCapacity
	}
	m.reserved++
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.reserved--; m.mu.Unlock() }()
	snapshot, err := m.store.Load(i.Scope)
	if err != nil {
		return Preparation{}, err
	}
	if snapshot.Revision != i.BindingRevision {
		return Preparation{}, ErrConflict
	}
	if len(snapshot.Bindings) != len(names) {
		return Preparation{}, ErrInvalidInput
	}
	for n, b := range snapshot.Bindings {
		if b.Name != names[n] {
			return Preparation{}, ErrInvalidInput
		}
	}
	if m.authorized(ctx, i, snapshot.Bindings) != nil {
		return Preparation{}, secrets.ErrDenied
	}
	p := &prepared{}
	keep := false
	defer func() {
		if !keep {
			erase(p)
		}
	}()
	for _, binding := range snapshot.Bindings {
		if ctx.Err() != nil {
			return Preparation{}, secrets.ErrCanceled
		}
		value, err := m.acquire.Acquire(ctx, selector(binding))
		if err != nil {
			value.Close()
			if safe, ok := err.(secrets.Error); ok {
				return Preparation{}, safe
			}
			return Preparation{}, secrets.ErrUnavailable
		}
		bytes := value.Bytes()
		value.Close()
		p.values = append(p.values, plaintext{binding.Name, func() []byte { return bytes }})
		p.size += len(bytes)
		if len(bytes) > secrets.MaxValueBytes || p.size > maxBundleBytes {
			return Preparation{}, ErrCapacity
		}
		p.metadata.Revisions = append(p.metadata.Revisions, Revision{binding.Name, binding.ConnectionID, value.Revision()})
	}
	current, err := m.store.Load(i.Scope)
	if err != nil {
		return Preparation{}, err
	}
	if current.Revision != snapshot.Revision {
		return Preparation{}, ErrConflict
	}
	if ctx.Err() != nil {
		return Preparation{}, secrets.ErrCanceled
	}
	if m.authorized(ctx, i, current.Bindings) != nil {
		return Preparation{}, secrets.ErrDenied
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Preparation{}, ErrUnavailable
	}
	p.metadata.ID = hex.EncodeToString(id[:])
	p.metadata.Identity = i
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeLocked()
	if m.closed {
		return Preparation{}, ErrUnavailable
	}
	if _, exists := m.pending[p.metadata.ID]; exists {
		return Preparation{}, ErrUnavailable
	}
	if m.bytes+p.size > maxPreparedBytes {
		return Preparation{}, ErrCapacity
	}
	p.metadata.ExpiresAt = m.now().Add(preparationTTL)
	m.pending[p.metadata.ID] = p
	m.bytes += p.size
	keep = true
	return clonePreparation(p.metadata), nil
}
func selector(b Binding) secrets.Selector {
	s := secrets.Selector{Version: secrets.SchemaVersion, Connection: b.ConnectionID}
	switch b.Kind {
	case ProviderVault:
		s.Vault = &secrets.VaultSelector{Path: b.Vault.Path, Key: b.Vault.Property, Revision: uint64(b.Vault.Version)}
	case ProviderInfisical:
		s.Infisical = &secrets.InfisicalSelector{Folder: b.Infisical.Folder, Key: b.Infisical.Key, Revision: uint64(b.Infisical.Version)}
	case ProviderLocal:
		s.File = &secrets.FileSelector{Source: b.LocalAlias}
	}
	return s
}

// Inspect returns metadata only for the exact reviewed identity/session. It
// still checks live authorization and binding state; a handle is not a grant.
func (m *Manager) Inspect(ctx context.Context, id string, i ReviewIdentity) (Preparation, error) {
	if m == nil || ctx == nil || !validID(id) || !validReview(i) {
		return Preparation{}, ErrInvalidInput
	}
	current, err := m.store.Load(i.Scope)
	if err != nil {
		return Preparation{}, err
	}
	if current.Revision != i.BindingRevision {
		return Preparation{}, ErrConflict
	}
	if ctx.Err() != nil || m.authorized(ctx, i, current.Bindings) != nil {
		return Preparation{}, secrets.ErrDenied
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeLocked()
	p := m.pending[id]
	if p == nil || p.metadata.Identity != i {
		return Preparation{}, ErrUnavailable
	}
	return clonePreparation(p.metadata), nil
}

// Take consumes a preparation once, before transport. Delivery ambiguity must
// be reconciled using the target operation journal; never call Prepare/Take as
// an automatic deployment retry. Caller must serialize binding changes and
// policy revocation with final dispatch: inspection alone cannot close a race.
func (m *Manager) Take(ctx context.Context, id string, i ReviewIdentity) (Preparation, *Delivery, error) {
	meta, err := m.Inspect(ctx, id, i)
	if err != nil {
		return Preparation{}, nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeLocked()
	p := m.pending[id]
	if p == nil || p.metadata.Identity != i {
		return Preparation{}, nil, ErrUnavailable
	}
	delete(m.pending, id)
	m.bytes -= p.size
	d := &Delivery{values: p.values}
	p.values = nil
	return meta, d, nil
}
func (m *Manager) Cancel(id string, i ReviewIdentity) error {
	if m == nil {
		return ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.pending[id]
	if p == nil || p.metadata.Identity != i {
		return ErrUnavailable
	}
	erase(p)
	m.bytes -= p.size
	delete(m.pending, id)
	return nil
}
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.stop()
	m.policy.Lock()
	defer m.policy.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for id, p := range m.pending {
		erase(p)
		delete(m.pending, id)
	}
	m.bytes = 0
}

// Compile-time guard against accidentally making Delivery JSON serializable.
var _ json.Marshaler = Delivery{}
