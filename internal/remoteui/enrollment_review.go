package remoteui

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"sync"
	"time"

	"github.com/umayr/brine/internal/targetregistry"
)

// enrollmentSnapshot is a value copy of precisely the records to be displayed.
// A nil Previous denotes an addition; updates must retain the exact old record.
// This is not an observation or a substitute for out-of-band verification.
type enrollmentSnapshot struct {
	Previous  *targetregistry.Record
	Candidate targetregistry.Record
	expires   time.Time
}

func (s enrollmentSnapshot) clone() enrollmentSnapshot {
	if s.Previous != nil {
		previous := *s.Previous
		s.Previous = &previous
	}
	return s
}

func (s enrollmentSnapshot) validate() error {
	if err := s.Candidate.Validate(); err != nil {
		return err
	}
	if s.Previous == nil {
		if s.Candidate.Revision != 1 {
			return errors.New("invalid new revision")
		}
		return nil
	}
	if err := s.Previous.Validate(); err != nil {
		return err
	}
	if s.Previous.ID != s.Candidate.ID || s.Previous.Revision == ^uint64(0) || s.Candidate.Revision != s.Previous.Revision+1 {
		return errors.New("invalid update")
	}
	return nil
}

// commitEnrollment accepts only a fresh authenticated observation matching the
// approved candidate byte-for-byte. Registry operations then compare-and-swap
// the exact previously displayed record. The caller must make the observation
// over a fresh pinned connection and independently verify the pins out of band.
func commitEnrollment(ctx context.Context, registry *targetregistry.Registry, approved enrollmentSnapshot, observed targetregistry.Record) error {
	if registry == nil {
		return errors.New("registry unavailable")
	}
	approved = approved.clone()
	if err := approved.validate(); err != nil {
		return err
	}
	if err := approved.Candidate.Verify(observed); err != nil {
		return err
	}
	if approved.Previous == nil {
		return registry.AddContext(ctx, observed, approved.Candidate)
	}
	return registry.UpdateContext(ctx, *approved.Previous, observed, targetregistry.ReviewedUpdate{Previous: *approved.Previous, Next: approved.Candidate})
}

type enrollmentApproval struct {
	snapshot enrollmentSnapshot
	session  [32]byte
	expires  time.Time
}

// enrollmentApprovals is process-local; restarts invalidate all approvals.
// Callers must authenticate the cookie and display this exact snapshot before
// exposing its token. Consumption is atomic and bound to the original session.
type enrollmentApprovals struct {
	mu    sync.Mutex
	key   [32]byte
	items map[string]enrollmentApproval
}

func newEnrollmentApprovals() (*enrollmentApprovals, error) {
	s := &enrollmentApprovals{items: make(map[string]enrollmentApproval)}
	if _, err := rand.Read(s.key[:]); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *enrollmentApprovals) session(cookie string) [32]byte {
	mac := hmac.New(sha256.New, s.key[:])
	_, _ = mac.Write([]byte(cookie))
	var sum [32]byte
	copy(sum[:], mac.Sum(nil))
	return sum
}

func (s *enrollmentApprovals) issue(cookie string, snapshot enrollmentSnapshot, now time.Time) (string, error) {
	if cookie == "" {
		return "", errors.New("session unavailable")
	}
	snapshot = snapshot.clone()
	if err := snapshot.validate(); err != nil {
		return "", err
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.items {
		if !now.Before(v.expires) {
			delete(s.items, k)
		}
	}
	if len(s.items) >= 1000 {
		return "", errors.New("review capacity")
	}
	if _, ok := s.items[token]; ok {
		return "", errors.New("review collision")
	}
	s.items[token] = enrollmentApproval{snapshot, s.session(cookie), now.Add(2 * time.Minute)}
	return token, nil
}

func (s *enrollmentApprovals) consume(cookie, token string, now time.Time) (enrollmentSnapshot, bool) {
	if cookie == "" {
		return enrollmentSnapshot{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[token]
	session := s.session(cookie)
	if !ok || !hmac.Equal(item.session[:], session[:]) {
		return enrollmentSnapshot{}, false
	}
	delete(s.items, token)
	if !now.Before(item.expires) {
		return enrollmentSnapshot{}, false
	}
	snapshot := item.snapshot.clone()
	snapshot.expires = item.expires
	return snapshot, true
}
