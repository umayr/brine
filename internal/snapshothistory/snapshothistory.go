// Package snapshothistory stores an explicitly selected, encrypted local archive
// of reviewed project snapshots. It is deliberately standalone: opening or
// initializing it neither enables a route nor sends, approves, restores, or
// executes anything.
package snapshothistory

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"

	"github.com/umayr/brine/internal/projectsnapshot"
)

const (
	// DefaultMaxBytes and DefaultMaxOperations are deliberately finite opt-in
	// limits. They count sealed archive bytes, including archive metadata and
	// retained failed-write remnants, rather than source byte length.
	DefaultMaxBytes      int64 = 2 << 30
	DefaultMaxOperations       = 100

	maxRestartDiscoveryResults    = 64
	maxIntentBytes                = 1 << 20
	maxRestartDiscoveryCiphertext = DefaultMaxBytes
)

var (
	ErrUnavailable     = errors.New("snapshot history unavailable")
	ErrCapacity        = errors.New("snapshot history capacity exhausted")
	ErrExists          = errors.New("snapshot history object already exists")
	ErrIndeterminate   = errors.New("snapshot history outcome indeterminate; inspect private state; do not retry")
	ErrDuplicateIntent = errors.New("snapshot history original journal already contains an attempt or provider request identity")
)

// ObjectID is an opaque caller-created identity. It is never inferred from an
// attempt, a request, a provider response, or archive contents.
type ObjectID [sha256.Size]byte

// Options names pre-provisioned local paths. ArchiveDir and the parent of
// KeyFile must already be separately operator-owned private directories.
// WorkspaceDir is required only to prove that the archive and key are outside
// the editable workspace; this package does not read it.
type Options struct {
	ArchiveDir    string
	KeyFile       string
	WorkspaceDir  string
	StoreID       [sha256.Size]byte
	MaxBytes      int64
	MaxOperations int
}

// Intent binds the exact local review to independent identities. AttemptID and
// ProviderRequestID must both be supplied and byte-distinct; neither is parsed
// as, nor ever replaced by, a provider operation/status/not-admitted value.
type Intent struct {
	AttemptID           []byte
	ProviderRequestID   []byte
	OriginalJournal     []byte
	ApprovedTargetScope []byte
	Attended            *AttendedIntent
}

// AttendedIntent authenticates the reviewed delivery context alongside the
// snapshot and original provider IDs. It contains no credential or proof token.
type AttendedIntent struct {
	Target            string `json:"target"`
	AuthorityRevision string `json:"authorityRevision"`
	Stack             string `json:"stack"`
	Mode              string `json:"mode"`
	Stage             string `json:"stage"`
	Selection         string `json:"selection"`
}

// CommitRequest contains the exact reviewed snapshot and its independent
// operation intent. Snapshot content is not redacted, normalized, or rebuilt.
type CommitRequest struct {
	ObjectID ObjectID
	Intent   Intent
	// ExpectedManifestDigest must be independently retained from review. Commit
	// recomputes the canonical Dill v1 value and rejects any mismatch before I/O.
	ExpectedManifestDigest [sha256.Size]byte
	Snapshot               projectsnapshot.Snapshot
}

// OpenRequest supplies the independently retained binding expected by a
// reader. Open rejects an archive object unless all these fields, including the
// canonical Dill v1 manifest commitment, authenticate exactly.
type OpenRequest struct {
	ObjectID       ObjectID
	Intent         Intent
	ManifestDigest [sha256.Size]byte
}

// Record is an independently authenticated exact snapshot and intent. Callers
// receive copies, so mutating a returned Record cannot change archived bytes.
type Record struct {
	ObjectID       ObjectID
	Intent         Intent
	ManifestDigest [sha256.Size]byte
	Snapshot       projectsnapshot.Snapshot
}

// RestartAuthorizer is supplied by the integration owner. It must authorize
// this exact opaque local target scope; nil, empty and wildcard scopes are not
// accepted by this package. Its approval is local archive access only, never a
// provider admission, outcome, fence, send, or execution authorization.
type RestartAuthorizer interface {
	AuthorizeSnapshotHistoryRestart(context.Context, []byte) error
}

// RestartDiscoveryRequest has deliberately no wildcard form. After is an
// authenticated opaque continuation for this store and target scope; an empty
// string starts the scan. Limit must be 1..64.
type RestartDiscoveryRequest struct {
	ApprovedTargetScope []byte
	Authorizer          RestartAuthorizer
	After               string
	Limit               int
}

// RestartMetadata contains authenticated intent metadata only. It never
// contains a snapshot, file path, file hash, source bytes, or an index derived
// from source plaintext.
type RestartMetadata struct {
	Locator           ObjectID
	AttemptID         []byte
	ProviderRequestID []byte
	OriginalJournal   []byte
	ManifestDigest    [sha256.Size]byte
}

// RestartDiscoveryPage returns authenticated results from one bounded scan.
// More means callers must present Next unchanged to continue, even when
// Records is empty. Pagination is not a snapshot across concurrent commits.
type RestartDiscoveryPage struct {
	Records []RestartMetadata
	Next    string
	More    bool
}

// Store holds the exclusive archive-writer lock until Close. It is safe for
// concurrent use by one process; a second cooperating process fails Open.
type Store struct {
	mu   sync.Mutex
	impl *store
}

// Initialize explicitly creates the one authenticated store-identity object in
// an existing, otherwise empty archive directory. It never generates, replaces,
// repairs, chmods, or writes the separately provisioned key.
func Initialize(ctx context.Context, options Options) error { return initialize(ctx, options) }

// Open opens only a previously initialized archive. It never initializes or
// repairs state, and it validates all committed objects and conservative
// remnants before making a Store available.
func Open(ctx context.Context, options Options) (*Store, error) {
	s, err := openStore(ctx, options)
	if err != nil {
		return nil, err
	}
	return &Store{impl: s}, nil
}

// Commit reserves the sealed-object size while holding the writer lock, then
// atomically publishes a no-clobber encrypted object. It returns only after a
// successful file and directory sync. An indeterminate error must not be
// retried or treated as not committed.
func (s *Store) Commit(ctx context.Context, request CommitRequest) (OpenRequest, error) {
	if s == nil {
		return OpenRequest{}, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.impl == nil {
		return OpenRequest{}, ErrUnavailable
	}
	return s.impl.commit(ctx, request)
}

// Open verifies the separately retained immutable context before returning
// exact snapshot content and intent. It never infers provider state.
func (s *Store) Open(ctx context.Context, request OpenRequest) (Record, error) {
	if s == nil {
		return Record{}, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.impl == nil {
		return Record{}, ErrUnavailable
	}
	return s.impl.open(ctx, request)
}

// DiscoverRestartMetadata scans only authenticated archive intent metadata for
// one exact locally authorized target scope. It has no route, network, provider
// state, snapshot, or restore behavior.
func (s *Store) DiscoverRestartMetadata(ctx context.Context, request RestartDiscoveryRequest) (RestartDiscoveryPage, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || request.Authorizer == nil || request.Limit <= 0 || request.Limit > maxRestartDiscoveryResults || len(request.ApprovedTargetScope) == 0 || len(request.ApprovedTargetScope) > maxIntentBytes {
		return RestartDiscoveryPage{}, ErrUnavailable
	}
	scope := append([]byte(nil), request.ApprovedTargetScope...)
	if err := request.Authorizer.AuthorizeSnapshotHistoryRestart(ctx, append([]byte(nil), scope...)); err != nil || ctx.Err() != nil {
		return RestartDiscoveryPage{}, ErrUnavailable
	}
	request.ApprovedTargetScope = scope
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.impl == nil {
		return RestartDiscoveryPage{}, ErrUnavailable
	}
	return s.impl.discoverRestartMetadata(ctx, request, maxRestartDiscoveryCiphertext)
}

// Close releases the exclusive process lock. It neither deletes archive data
// nor cleans up pending remnants.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.impl == nil {
		return nil
	}
	err := s.impl.close()
	s.impl = nil
	return err
}
