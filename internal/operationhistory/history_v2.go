package operationhistory

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/umayr/brine/internal/dill"
)

const VersionV2 = "brine.operation/2"
const MaxRecordV2 = 512 * 1024

// SourceRef is a producer assertion, not runtime or cryptographic proof.
// It contains no source, path, evaluator settings, or environment.
type SourceRef struct {
	SnapshotDigest         string `json:"snapshotDigest"`
	EvaluatorProfileDigest string `json:"evaluatorProfileDigest"`
	EvaluatorRefDigest     string `json:"evaluatorRefDigest"`
}
type BoundIntent struct {
	Action         string               `json:"action"`
	Stack          string               `json:"stack"`
	Actor          string               `json:"actor"`
	Source         SourceRef            `json:"source"`
	ExpectedTarget dill.PreflightTarget `json:"expectedTarget"`
}
type RecordV2 struct {
	Version            string                    `json:"version"`
	RequestID          string                    `json:"requestId"`
	Kind               string                    `json:"kind"`
	Intent             BoundIntent               `json:"intent"`
	PreviewRequestID   string                    `json:"previewRequestId"`
	ReviewDigest       string                    `json:"reviewDigest"`
	AcceptedAt         time.Time                 `json:"acceptedAt"`
	StartedAt          *time.Time                `json:"startedAt"`
	EndObservedAt      *time.Time                `json:"endObservedAt"`
	RecoveryObservedAt *time.Time                `json:"recoveryObservedAt"`
	Phase              string                    `json:"phase"`
	Preview            *dill.PreflightInvocation `json:"preview"`
	Apply              *dill.OperationInvocation `json:"apply"`
	Persistence        string                    `json:"persistence"`
	Delivery           string                    `json:"delivery"`
}

type reviewContext struct {
	Version string                `json:"version"`
	Intent  BoundIntent           `json:"intent"`
	Result  *dill.PreflightResult `json:"result"`
}

func reviewDigest(i BoundIntent, result *dill.PreflightResult) string {
	b, _ := json.Marshal(reviewContext{Version: "brine.review/1", Intent: i, Result: result})
	h := sha256.Sum256(append([]byte("brine.review/1\x00"), b...))
	return "sha256:" + hex.EncodeToString(h[:])
}
func validIntentV2(i BoundIntent) bool {
	t := i.ExpectedTarget
	return action(i.Action) && stackPattern.MatchString(i.Stack) && digestPattern.MatchString(i.Actor) && digestPattern.MatchString(i.Source.SnapshotDigest) && digestPattern.MatchString(i.Source.EvaluatorProfileDigest) && digestPattern.MatchString(i.Source.EvaluatorRefDigest) && t.Engine == "docker" && digestPattern.MatchString(t.EndpointDigest) && digestPattern.MatchString(t.QualifiedIdentityDigest)
}
func decodePreview(v *dill.PreflightInvocation) (*dill.PreflightInvocation, error) {
	if v == nil || (v.ProcessExit != nil && (*v.ProcessExit < -1 || *v.ProcessExit > 255)) {
		return nil, ErrUnsafe
	}
	if v.Result == nil {
		if v.Status != "unknown" {
			return nil, ErrUnsafe
		}
		return &dill.PreflightInvocation{ProcessExit: v.ProcessExit, Status: "unknown"}, nil
	}
	// The provider result, not its enclosing history/invocation JSON, has the
	// 262144-byte producer bound. Do not spend that budget on our envelope.
	b, e := json.Marshal(v.Result)
	if e != nil || len(b) > dill.PreflightMaxOutputBytes {
		return nil, ErrUnsafe
	}
	x := dill.ObservePreflightInvocation(b, v.ProcessExit)
	if x.Result == nil || x.Status != v.Status {
		return nil, ErrUnsafe
	}
	return &x, nil
}
func validV2(r RecordV2) bool {
	if r.Version != VersionV2 || !ValidID(r.RequestID) || !validIntentV2(r.Intent) || r.AcceptedAt.IsZero() || r.Persistence != "durable" || (r.Delivery != "not_observed" && r.Delivery != "attempted_unconfirmed" && r.Delivery != "write_failed") {
		return false
	}
	if r.Kind != "preview" && r.Kind != "apply" {
		return false
	}
	if r.Kind == "apply" {
		if !ValidID(r.PreviewRequestID) || r.PreviewRequestID == r.RequestID || !digestPattern.MatchString(r.ReviewDigest) || r.Preview != nil {
			return false
		}
	} else if r.PreviewRequestID != "" || r.Apply != nil {
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
		if r.StartedAt != nil || r.EndObservedAt != nil || r.RecoveryObservedAt != nil || r.Preview != nil || r.Apply != nil {
			return false
		}
	case "running":
		if r.StartedAt == nil || r.EndObservedAt != nil || r.RecoveryObservedAt != nil || r.Preview != nil || r.Apply != nil {
			return false
		}
	case "unknown":
		if r.EndObservedAt != nil || r.RecoveryObservedAt == nil || r.RecoveryObservedAt.Before(r.AcceptedAt) || r.Preview != nil || r.Apply != nil {
			return false
		}
	case "terminal":
		if r.StartedAt == nil || r.EndObservedAt == nil || r.RecoveryObservedAt != nil {
			return false
		}
	default:
		return false
	}
	if r.Kind == "preview" {
		if r.Phase != "terminal" {
			return r.ReviewDigest == ""
		}
		v, e := decodePreview(r.Preview)
		if e != nil {
			return false
		}
		if v.Result != nil {
			p := v.Result
			if (p.Operation != "" && p.Operation != r.Intent.Action) || (p.Stack != "" && p.Stack != r.Intent.Stack) || (p.Target != nil && *p.Target != r.Intent.ExpectedTarget) {
				return false
			}
		}
		if v.Result != nil && v.Status == "succeeded" {
			t := r.Intent.ExpectedTarget
			if dill.CheckPreflightBinding(v, dill.PreflightBinding{Stack: r.Intent.Stack, Operation: r.Intent.Action, Engine: t.Engine, EndpointDigest: t.EndpointDigest, QualifiedIdentityDigest: t.QualifiedIdentityDigest}) != nil {
				return false
			}
			return r.ReviewDigest == reviewDigest(r.Intent, v.Result)
		}
		return r.ReviewDigest == ""
	}
	if r.Phase == "terminal" {
		if r.Apply == nil {
			return false
		}
		b, e := json.Marshal(r.Apply)
		if e != nil {
			return false
		}
		v, e := dill.DecodeBoundOperationInvocation(b)
		if e != nil {
			return false
		}
		if v.Receipt != nil {
			t := v.Receipt.Target
			if (v.Receipt.Outcome == "succeeded" || v.Receipt.Effects != "none") && (t == nil || t.QualifiedIdentityDigest != r.Intent.ExpectedTarget.QualifiedIdentityDigest) {
				return false
			}
			if v.Receipt.Operation != r.Intent.Action || (v.Receipt.Stack != "" && v.Receipt.Stack != r.Intent.Stack) || (t != nil && (t.Engine != "docker" || t.EndpointDigest != r.Intent.ExpectedTarget.EndpointDigest || (t.QualifiedIdentityDigest != "" && t.QualifiedIdentityDigest != r.Intent.ExpectedTarget.QualifiedIdentityDigest))) {
				return false
			}
		}
	}
	return true
}
func decodeV2(b []byte) (RecordV2, error) {
	var r RecordV2
	if len(b) > MaxRecordV2 || json.Unmarshal(b, &r) != nil || !validV2(r) {
		return r, ErrUnsafe
	}
	c, e := json.Marshal(r)
	if e != nil || !bytes.Equal(c, b) {
		return r, ErrUnsafe
	}
	return r, nil
}
func validStoredRecord(id string, b []byte) bool {
	if r, e := decode(b); e == nil {
		return r.RequestID == id
	}
	r, e := decodeV2(b)
	return e == nil && r.RequestID == id
}
func (s *Store) loadV2(id string) (RecordV2, error) {
	b, e := s.disk.read(id + ".json")
	if e != nil {
		return RecordV2{}, e
	}
	r, e := decodeV2(b)
	if e != nil || r.RequestID != id {
		return RecordV2{}, ErrUnsafe
	}
	return r, nil
}
func (s *Store) saveV2(r RecordV2) error {
	if !validV2(r) {
		return ErrUnsafe
	}
	b, e := json.Marshal(r)
	if e != nil || len(b) > MaxRecordV2 {
		return ErrUnsafe
	}
	return s.disk.write(r.RequestID+".json", b)
}
func cloneV2(r RecordV2) RecordV2 {
	b, _ := json.Marshal(r)
	var v RecordV2
	_ = json.Unmarshal(b, &v)
	return v
}
func (s *Store) GetV2(id string) (RecordV2, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidID(id) {
		return RecordV2{}, os.ErrNotExist
	}
	if r, ok := s.knownV2[id]; ok {
		return cloneV2(r), nil
	}
	r, e := s.loadV2(id)
	return cloneV2(r), e
}
func (s *Store) validateReviewLink(i BoundIntent, id, digest string) error {
	if !ValidID(id) || !digestPattern.MatchString(digest) {
		return ErrUnsafe
	}
	p, e := s.loadV2(id)
	if e != nil || p.Kind != "preview" || p.Phase != "terminal" || p.ReviewDigest != digest || p.ReviewDigest == "" || p.Intent != i {
		return ErrUnsafe
	}
	return nil
}
func (s *Store) BeginPreviewV2(i BoundIntent) (*RequestV2, error) {
	return s.beginV2(i, "preview", "", "")
}
func (s *Store) BeginApplyV2(i BoundIntent, previewID, digest string) (*RequestV2, error) {
	return s.beginV2(i, "apply", previewID, digest)
}
func (s *Store) beginV2(i BoundIntent, kind, previewID, digest string) (*RequestV2, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed || !validIntentV2(i) {
		return nil, ErrUnsafe
	}
	if kind == "apply" && s.validateReviewLink(i, previewID, digest) != nil {
		return nil, ErrUnsafe
	}
	names, e := s.disk.names()
	if e != nil || len(names) >= MaxRecords {
		s.failed = true
		return nil, ErrUnsafe
	}
	for _, n := range names {
		id := strings.TrimSuffix(n, ".json")
		if n != id+".json" || !ValidID(id) {
			s.failed = true
			return nil, ErrUnsafe
		}
		b, e := s.disk.read(n)
		if e != nil || !validStoredRecord(id, b) {
			s.failed = true
			return nil, ErrUnsafe
		}
		if kind == "apply" {
			if prior, err := decodeV2(b); err == nil && prior.Kind == "apply" && prior.PreviewRequestID == previewID {
				return nil, ErrUnsafe
			}
		}
	}
	var random [16]byte
	if _, e = rand.Read(random[:]); e != nil {
		return nil, ErrUnsafe
	}
	r := RecordV2{Version: VersionV2, RequestID: "br_" + hex.EncodeToString(random[:]), Kind: kind, Intent: i, PreviewRequestID: previewID, ReviewDigest: digest, AcceptedAt: time.Now().UTC(), Phase: "accepted", Persistence: "durable", Delivery: "not_observed"}
	if _, e = s.disk.read(r.RequestID + ".json"); !errors.Is(e, os.ErrNotExist) {
		return nil, ErrUnsafe
	}
	if s.saveV2(r) != nil {
		s.failed = true
		return nil, ErrUnsafe
	}
	return &RequestV2{store: s, id: r.RequestID, last: cloneV2(r)}, nil
}

type RequestV2 struct {
	store *Store
	id    string
	last  RecordV2
}

func (r *RequestV2) ID() string { return r.id }
func (r *RequestV2) update(change func(*RecordV2) error) error {
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	current := cloneV2(r.last)
	if change(&current) != nil || !validV2(current) {
		return ErrUnsafe
	}
	_, e := s.loadV2(r.id)
	if s.failed || e != nil || s.saveV2(current) != nil {
		s.failed = true
		current.Persistence = "failed_inspect"
		if s.knownV2 == nil {
			s.knownV2 = map[string]RecordV2{}
		}
		s.knownV2[r.id] = cloneV2(current)
		r.last = cloneV2(current)
		return ErrUnsafe
	}
	r.last = cloneV2(current)
	return nil
}
func (r *RequestV2) Started() error {
	return r.update(func(v *RecordV2) error {
		if v.Phase != "accepted" {
			return ErrUnsafe
		}
		now := time.Now().UTC()
		v.StartedAt = &now
		v.Phase = "running"
		return nil
	})
}
func (r *RequestV2) TerminalPreview(inv dill.PreflightInvocation) error {
	v, e := decodePreview(&inv)
	if e != nil {
		return ErrUnsafe
	}
	return r.update(func(x *RecordV2) error {
		if x.Kind != "preview" || x.Phase != "running" {
			return ErrUnsafe
		}
		now := time.Now().UTC()
		x.EndObservedAt = &now
		x.Phase = "terminal"
		x.Preview = v
		if v.Status == "succeeded" && v.Result != nil {
			x.ReviewDigest = reviewDigest(x.Intent, v.Result)
		}
		return nil
	})
}
func (r *RequestV2) TerminalApply(inv dill.OperationInvocation) error {
	b, e := json.Marshal(inv)
	if e != nil {
		return ErrUnsafe
	}
	v, e := dill.DecodeBoundOperationInvocation(b)
	if e != nil {
		return ErrUnsafe
	}
	return r.update(func(x *RecordV2) error {
		if x.Kind != "apply" || x.Phase != "running" {
			return ErrUnsafe
		}
		now := time.Now().UTC()
		x.EndObservedAt = &now
		x.Phase = "terminal"
		x.Apply = v
		return nil
	})
}
func (r *RequestV2) Delivery(failed bool) error {
	return r.update(func(v *RecordV2) error {
		v.Delivery = "attempted_unconfirmed"
		if failed {
			v.Delivery = "write_failed"
		}
		return nil
	})
}
