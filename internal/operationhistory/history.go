// Package operationhistory owns neutral, private Brine request history. It is
// neither Dill coordination state nor secret/controller state and cannot replay.
package operationhistory

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/umayr/brine/internal/dill"
)

const Version = "brine.operation/1"
const MaxRecord = 3 * 1024 * 1024
const MaxRecords = 4096

var ErrUnsafe = errors.New("operation history unavailable; inspect private state; do not retry")
var idPattern = regexp.MustCompile(`^br_[a-f0-9]{32}$`)
var stackPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var entryPattern = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]{0,127}\.pkl$`)
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func ValidID(id string) bool { return idPattern.MatchString(id) }
func Digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Record is a closed private schema. Configured target digests are assertions,
// separate from the actual validated receipt. UNBOUND deliberately makes no
// immutable source, review, inventory or evaluator-policy claim.
type Record struct {
	Version                string                    `json:"version"`
	RequestID              string                    `json:"requestId"`
	Action                 string                    `json:"action"`
	Stack                  string                    `json:"stack"`
	Actor                  string                    `json:"actor"`
	SourceBinding          string                    `json:"sourceBinding"`
	Entrypoint             string                    `json:"entrypoint,omitempty"`
	SelectorDigest         string                    `json:"selectorDigest"`
	ExpectedIdentityDigest string                    `json:"expectedIdentityDigest"`
	AcceptedAt             time.Time                 `json:"acceptedAt"`
	StartedAt              *time.Time                `json:"startedAt"`
	EndObservedAt          *time.Time                `json:"endObservedAt"`
	RecoveryObservedAt     *time.Time                `json:"recoveryObservedAt"`
	Phase                  string                    `json:"phase"`
	Status                 string                    `json:"status"`
	DillOperationID        *string                   `json:"dillOperationId"`
	Invocation             *dill.OperationInvocation `json:"invocation"`
	Persistence            string                    `json:"persistence"`
	Delivery               string                    `json:"delivery"`
	Reason                 string                    `json:"reason"`
}

type Intent struct{ Action, Stack, Actor, Entrypoint, SelectorDigest, ExpectedIdentityDigest string }
type backend interface {
	read(string) ([]byte, error)
	write(string, []byte) error
	names() ([]string, error)
	close() error
}
type Store struct {
	mu     sync.Mutex
	disk   backend
	failed bool
	known  map[string]Record
}

// SeparateRoots rejects aliases and nesting in either direction. Data may be a
// prospective path; private state roots themselves must already exist.
func SeparateRoots(root, data, coordination string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return ErrUnsafe
	}
	for _, other := range []string{data, coordination} {
		if !filepath.IsAbs(other) || filepath.Clean(other) != other {
			return ErrUnsafe
		}
		canonical, e := canonicalPath(other)
		if e != nil {
			return ErrUnsafe
		}
		for _, pair := range [][2]string{{root, canonical}, {canonical, root}} {
			rel, e := filepath.Rel(pair[0], pair[1])
			if e != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
				return ErrUnsafe
			}
		}
	}
	return nil
}
func canonicalPath(path string) (string, error) {
	p, e := filepath.EvalSymlinks(path)
	if e == nil {
		return p, nil
	}
	if !errors.Is(e, os.ErrNotExist) || filepath.Dir(path) == path {
		return "", ErrUnsafe
	}
	p, e = canonicalPath(filepath.Dir(path))
	if e != nil {
		return "", e
	}
	return filepath.Join(p, filepath.Base(path)), nil
}

func Open(root, data, coordination string) (*Store, error) {
	if SeparateRoots(root, data, coordination) != nil {
		return nil, ErrUnsafe
	}
	disk, e := openBackend(root)
	if e != nil {
		return nil, ErrUnsafe
	}
	s := &Store{disk: disk, known: make(map[string]Record)}
	names, e := disk.names()
	if e != nil || len(names) > MaxRecords {
		disk.close()
		return nil, ErrUnsafe
	}
	// Validate the entire store before recovery modifies any accepted record.
	records := make([]Record, 0, len(names))
	for _, n := range names {
		if !strings.HasSuffix(n, ".json") || !ValidID(strings.TrimSuffix(n, ".json")) {
			disk.close()
			return nil, ErrUnsafe
		}
		r, e := s.load(strings.TrimSuffix(n, ".json"))
		if e != nil {
			disk.close()
			return nil, ErrUnsafe
		}
		records = append(records, r)
	}
	now := time.Now().UTC()
	for _, r := range records {
		if r.Phase == "accepted" || r.Phase == "running" {
			r.Phase = "unknown"
			r.Status = "unknown"
			r.Reason = "restart_requires_inspection"
			r.RecoveryObservedAt = &now
			// No completion timestamp, PID action, replay or synthetic Dill ID.
			if s.save(r) != nil {
				disk.close()
				return nil, ErrUnsafe
			}
		}
	}
	return s, nil
}
func (s *Store) Close() error { s.mu.Lock(); defer s.mu.Unlock(); return s.disk.close() }
func action(v string) bool {
	return v == "up" || v == "down" || v == "stop" || v == "start" || v == "restart"
}
func valid(r Record) bool {
	if r.Version != Version || !ValidID(r.RequestID) || !action(r.Action) || !stackPattern.MatchString(r.Stack) || !digestPattern.MatchString(r.Actor) || r.SourceBinding != "UNBOUND" || (r.Entrypoint != "" && !entryPattern.MatchString(r.Entrypoint)) || !digestPattern.MatchString(r.SelectorDigest) || !digestPattern.MatchString(r.ExpectedIdentityDigest) || r.AcceptedAt.IsZero() || r.Persistence != "durable" {
		return false
	}
	if r.Delivery != "not_observed" && r.Delivery != "attempted_unconfirmed" && r.Delivery != "write_failed" {
		return false
	}
	if r.StartedAt != nil && r.StartedAt.Before(r.AcceptedAt) {
		return false
	}
	if r.EndObservedAt != nil && (r.StartedAt == nil || r.EndObservedAt.Before(*r.StartedAt)) {
		return false
	}
	switch r.Phase {
	case "accepted":
		if r.Status != "accepted" || r.StartedAt != nil || r.EndObservedAt != nil || r.Invocation != nil || r.Reason != "accepted" {
			return false
		}
	case "running":
		if r.Status != "running" || r.StartedAt == nil || r.EndObservedAt != nil || r.Invocation != nil || r.Reason != "running" {
			return false
		}
	case "terminal":
		if r.Invocation == nil || r.StartedAt == nil || r.EndObservedAt == nil || r.Reason != "terminal_observed" {
			return false
		}
	case "unknown":
		if r.Status != "unknown" || r.EndObservedAt != nil || r.RecoveryObservedAt == nil || r.Invocation != nil || r.Reason != "restart_requires_inspection" {
			return false
		}
	default:
		return false
	}
	if r.RecoveryObservedAt != nil && (r.Phase != "unknown" || r.RecoveryObservedAt.Before(r.AcceptedAt)) {
		return false
	}
	if r.Invocation != nil {
		b, e := json.Marshal(r.Invocation)
		if e != nil {
			return false
		}
		v, e := dill.DecodeOperationInvocation(b)
		if e != nil || v.Status != r.Status {
			return false
		}
		if v.Receipt != nil {
			if r.DillOperationID == nil || *r.DillOperationID != v.Receipt.OperationID || v.Receipt.Operation != r.Action || (v.Receipt.Stack != "" && v.Receipt.Stack != r.Stack) {
				return false
			}
			if t := v.Receipt.Target; t != nil && (t.Engine != "docker" || t.EndpointDigest != r.SelectorDigest || (t.QualifiedIdentityDigest != "" && t.QualifiedIdentityDigest != r.ExpectedIdentityDigest)) {
				return false
			}
		} else if r.DillOperationID != nil {
			return false
		}
	} else if r.DillOperationID != nil {
		return false
	}
	return true
}
func decode(b []byte) (Record, error) {
	var r Record
	if len(b) > MaxRecord || json.Unmarshal(b, &r) != nil || !valid(r) {
		return r, ErrUnsafe
	}
	canonical, e := json.Marshal(r)
	// Exact canonical encoding rejects unknown/duplicate/missing fields, alternate
	// field spellings, noncanonical dates and trailing data without repair.
	if e != nil || !bytes.Equal(canonical, b) {
		return r, ErrUnsafe
	}
	return r, nil
}
func (s *Store) load(id string) (Record, error) {
	b, e := s.disk.read(id + ".json")
	if e != nil {
		return Record{}, e
	}
	r, e := decode(b)
	if e != nil || r.RequestID != id {
		return Record{}, ErrUnsafe
	}
	return r, nil
}
func (s *Store) save(r Record) error {
	if !valid(r) {
		return ErrUnsafe
	}
	b, e := json.Marshal(r)
	if e != nil || len(b) > MaxRecord {
		return ErrUnsafe
	}
	return s.disk.write(r.RequestID+".json", b)
}
func (s *Store) Get(id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidID(id) {
		return Record{}, os.ErrNotExist
	}
	if r, ok := s.known[id]; ok {
		return clone(r), nil
	}
	r, e := s.load(id)
	return clone(r), e
}
func clone(r Record) Record {
	b, _ := json.Marshal(r)
	var copy Record
	_ = json.Unmarshal(b, &copy)
	return copy
}
func (s *Store) Begin(i Intent) (*Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return nil, ErrUnsafe
	}
	names, e := s.disk.names()
	if e != nil || len(names) >= MaxRecords {
		s.failed = true
		return nil, ErrUnsafe
	}
	for _, name := range names {
		id := strings.TrimSuffix(name, ".json")
		if name != id+".json" || !ValidID(id) {
			s.failed = true
			return nil, ErrUnsafe
		}
		if _, e := s.load(id); e != nil {
			s.failed = true
			return nil, ErrUnsafe
		}
	}
	var random [16]byte
	if _, e = rand.Read(random[:]); e != nil {
		return nil, ErrUnsafe
	}
	r := Record{Version: Version, RequestID: "br_" + hex.EncodeToString(random[:]), Action: i.Action, Stack: i.Stack, Actor: i.Actor, SourceBinding: "UNBOUND", SelectorDigest: i.SelectorDigest, ExpectedIdentityDigest: i.ExpectedIdentityDigest, AcceptedAt: time.Now().UTC(), Phase: "accepted", Status: "accepted", Persistence: "durable", Delivery: "not_observed", Reason: "accepted"}
	// Entrypoint is optional known relative metadata, never a host path/source.
	if entryPattern.MatchString(i.Entrypoint) {
		r.Entrypoint = i.Entrypoint
	}
	if _, e = s.disk.read(r.RequestID + ".json"); !errors.Is(e, os.ErrNotExist) {
		return nil, ErrUnsafe
	}
	if s.save(r) != nil {
		s.failed = true
		return nil, ErrUnsafe
	}
	return &Request{store: s, id: r.RequestID, last: clone(r)}, nil
}

type Request struct {
	store *Store
	id    string
	last  Record
}

func (r *Request) ID() string { return r.id }
func (r *Request) update(change func(*Record) error) error {
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	current := clone(r.last)
	if change(&current) != nil {
		return ErrUnsafe
	}
	// Keep typed known axes in memory even if the prior durable record became
	// unreadable or a previous write failed. Never reinterpret this as success.
	_, loadErr := s.load(r.id)
	if s.failed || loadErr != nil || s.save(current) != nil {
		s.failed = true
		current.Persistence = "failed_inspect"
		current.Reason = "history_failure_requires_inspection"
		s.known[r.id] = clone(current)
		r.last = clone(current)
		return ErrUnsafe
	}
	r.last = clone(current)
	return nil
}
func (r *Request) Started() error {
	return r.update(func(v *Record) error {
		if v.Phase != "accepted" {
			return ErrUnsafe
		}
		now := time.Now().UTC()
		v.StartedAt = &now
		v.Phase = "running"
		v.Status = "running"
		v.Reason = "running"
		return nil
	})
}
func (r *Request) Terminal(v dill.OperationInvocation) error {
	// Validation belongs to the Runner adapter. Revalidation protects persistence
	// against accidental misuse, never upgrades an untrusted HTTP marker.
	b, e := json.Marshal(v)
	if e != nil {
		return ErrUnsafe
	}
	verified, e := dill.DecodeOperationInvocation(b)
	if e != nil {
		return ErrUnsafe
	}
	return r.update(func(record *Record) error {
		if record.Phase != "running" {
			return ErrUnsafe
		}
		now := time.Now().UTC()
		record.EndObservedAt = &now
		record.Phase = "terminal"
		record.Status = verified.Status
		record.Invocation = verified
		record.Reason = "terminal_observed"
		if verified.Receipt != nil {
			id := verified.Receipt.OperationID
			record.DillOperationID = &id
		}
		return nil
	})
}

// Delivery observes only local HTTP write completion, never browser receipt.
func (r *Request) Delivery(failed bool) error {
	return r.update(func(v *Record) error {
		v.Delivery = "attempted_unconfirmed"
		if failed {
			v.Delivery = "write_failed"
		}
		return nil
	})
}
