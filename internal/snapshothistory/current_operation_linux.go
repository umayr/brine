//go:build linux

package snapshothistory

import (
	"context"
	"crypto/sha256"
	"strings"
)

// Hashing scope keeps summary memory independent of historical opaque scope
// sizes. A collision can conservatively block another scope; the write-side
// inventory still compares original scope bytes exactly.
type attendedKey struct {
	scope [sha256.Size]byte
	stack string
}

func operationKey(scope []byte, stack string) attendedKey {
	return attendedKey{scope: sha256.Sum256(scope), stack: stack}
}

// Only unresolved objects are retained. One immutable object contributes at
// most once and can only remove its own contribution after durable receipt.
type attendedSummary struct {
	objects map[ObjectID]attendedKey
	counts  map[attendedKey]int
}

func newAttendedSummary() *attendedSummary {
	return &attendedSummary{objects: make(map[ObjectID]attendedKey), counts: make(map[attendedKey]int)}
}

func (s *attendedSummary) add(id ObjectID, key attendedKey) bool {
	if _, exists := s.objects[id]; exists || len(s.objects) >= maxArchiveEntries-1 {
		return false
	}
	key.stack = strings.Clone(key.stack)
	s.objects[id] = key
	s.counts[key]++
	return true
}

func (s *attendedSummary) contains(id ObjectID, key attendedKey) bool {
	found, ok := s.objects[id]
	return ok && found == key && s.counts[key] > 0
}

func (s *attendedSummary) remove(id ObjectID, key attendedKey) {
	delete(s.objects, id)
	if s.counts[key] == 1 {
		delete(s.counts, key)
	} else {
		s.counts[key]--
	}
}

func (s *store) checkAttendedIdle(ctx context.Context, scope, stack string) error {
	// No filesystem calls on the preview path. Tampering with or replacing the
	// trusted live archive is outside this summary's trust boundary; exact
	// archive reads and final Commit still validate the on-disk state.
	if ctx.Err() != nil || s.failed || s.closed || s.attended == nil {
		return ErrUnavailable
	}
	if s.attended.counts[operationKey([]byte(scope), stack)] != 0 {
		return ErrUnresolvedOperation
	}
	return nil
}
