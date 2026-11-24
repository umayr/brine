//go:build linux

package snapshothistory

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/projectsnapshot"
	"golang.org/x/crypto/hkdf"
	"golang.org/x/sys/unix"
)

const (
	identityName               = ".brine-snapshothistory.identity"
	objectPrefix               = "object-"
	pendingPrefix              = ".pending-"
	formatVersion              = byte(1)
	payloadVersion             = "brine.snapshot-history/1"
	maxObjectCiphertextBytes   = 32 << 20
	maxArchiveEntries          = 4096
	directoryBatchEntries      = 128
	maxRestartDiscoveryObjects = 256
	restartCursorBytes         = 5 + 12 + 2*sha256.Size + 16
)

// Test-only fault seams. Production code never assigns them; they prove
// publication boundaries and a real directory-sync error without a runtime knob.
var testFault func(string) error
var testDirectorySync func(int) error

type store struct {
	mu                 sync.Mutex
	root               *os.File
	rootPath           string
	key                [32]byte
	storeID            [32]byte
	maxBytes           int64
	maxOperations      int
	failed, closed     bool
	reservedBytes      int64
	reservedOperations int
	attended           *attendedSummary // published only after successful authenticated Open
}

type payload struct {
	Version             string                   `json:"version"`
	ObjectID            string                   `json:"objectId"`
	AttemptID           []byte                   `json:"attemptId"`
	ProviderRequestID   []byte                   `json:"providerRequestId"`
	OriginalJournal     []byte                   `json:"originalJournal"`
	ApprovedTargetScope []byte                   `json:"approvedTargetScope"`
	Attended            *AttendedIntent          `json:"attended,omitempty"`
	ManifestDigest      string                   `json:"manifestDigest"`
	Snapshot            projectsnapshot.Snapshot `json:"snapshot"`
}

type identity struct {
	Version string `json:"version"`
	StoreID string `json:"storeId"`
}

type usage struct {
	bytes      int64
	operations int
	entries    int
	duplicate  bool
	unresolved bool
}

func initialize(ctx context.Context, options Options) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrUnavailable
	}
	root, key, normalized, err := validateOptions(options)
	if err != nil {
		return ErrUnavailable
	}
	defer root.Close()
	if unix.Flock(int(root.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return ErrUnavailable
	}
	defer unix.Flock(int(root.Fd()), unix.LOCK_UN)
	if err := checkDirectory(root, true); err != nil {
		return ErrUnavailable
	}
	names, err := directoryNames(root)
	if err != nil || len(names) != 0 {
		return ErrUnavailable
	}
	plain, err := json.Marshal(identity{Version: payloadVersion, StoreID: hex.EncodeToString(normalized.StoreID[:])})
	if err != nil {
		return ErrUnavailable
	}
	sealed, err := seal(key, normalized.StoreID, ObjectID{}, "identity", plain)
	if err != nil {
		return ErrUnavailable
	}
	if int64(len(sealed)) > normalized.MaxBytes {
		return ErrCapacity
	}
	if err := writeNoClobber(root, identityName, sealed); err != nil {
		return err
	}
	return nil
}

func openStore(ctx context.Context, options Options) (*store, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	root, key, normalized, err := validateOptions(options)
	if err != nil {
		return nil, ErrUnavailable
	}
	if unix.Flock(int(root.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		root.Close()
		return nil, ErrUnavailable
	}
	s := &store{root: root, rootPath: normalized.ArchiveDir, key: key, storeID: normalized.StoreID, maxBytes: normalized.MaxBytes, maxOperations: normalized.MaxOperations}
	if err := s.check(); err != nil {
		s.close()
		return nil, ErrUnavailable
	}
	if err := s.verifyIdentity(); err != nil {
		s.close()
		return nil, ErrUnavailable
	}
	// Existing valid retained data remains readable if an operator later lowers
	// a quota. Only a new Commit is refused by the reduced cap.
	summary := newAttendedSummary()
	if _, err := s.scanWithSummary(ctx, nil, summary); err != nil || ctx.Err() != nil {
		s.close()
		return nil, ErrUnavailable
	}
	// A prior publication may have been visible but indeterminate after rename.
	// Sync the directory before using a reopened receipt to clear a blocker.
	if err := unix.Fsync(int(root.Fd())); err != nil || ctx.Err() != nil {
		s.close()
		return nil, ErrUnavailable
	}
	s.attended = summary
	return s, nil
}

func validateOptions(options Options) (*os.File, [32]byte, Options, error) {
	var zero [32]byte
	if options.MaxBytes == 0 {
		options.MaxBytes = DefaultMaxBytes
	}
	if options.MaxOperations == 0 {
		options.MaxOperations = DefaultMaxOperations
	}
	if options.MaxBytes <= 0 || options.MaxOperations <= 0 || options.MaxOperations >= maxArchiveEntries || options.StoreID == zero {
		return nil, zero, options, ErrUnavailable
	}
	for _, p := range []*string{&options.ArchiveDir, &options.KeyFile, &options.WorkspaceDir} {
		if !filepath.IsAbs(*p) || filepath.Clean(*p) != *p || *p == "/" {
			return nil, zero, options, ErrUnavailable
		}
	}
	// These paths are required to be literal, symlink-free canonical paths. The
	// descriptor traversal below proves that for paths which hold private state.
	for _, pair := range [][2]string{{options.ArchiveDir, options.WorkspaceDir}, {options.KeyFile, options.ArchiveDir}, {options.KeyFile, options.WorkspaceDir}} {
		if overlaps(pair[0], pair[1]) {
			return nil, zero, options, ErrUnavailable
		}
	}
	root, err := openPrivateDirectory(options.ArchiveDir)
	if err != nil {
		return nil, zero, options, ErrUnavailable
	}
	key, err := readKey(options.KeyFile)
	if err != nil {
		root.Close()
		return nil, zero, options, ErrUnavailable
	}
	// Check WorkspaceDir through no-follow traversal too. It is not opened for
	// contents, only so a symlink alias cannot defeat the non-overlap assertion.
	workspace, err := openTrustedDirectory(options.WorkspaceDir, false)
	if err != nil {
		root.Close()
		return nil, zero, options, ErrUnavailable
	}
	workspace.Close()
	return root, key, options, nil
}

func overlaps(a, b string) bool {
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
			return true
		}
	}
	return false
}

func aclSafe(fd int) bool {
	for _, name := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		if _, err := unix.Fgetxattr(fd, name, nil); err != unix.ENODATA {
			return false
		}
	}
	return true
}
func checkDirectory(f *os.File, private bool) error { return checkDirectoryFD(int(f.Fd()), private) }
func checkDirectoryFD(fd int, private bool) error {
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&0022 != 0 || !aclSafe(fd) {
		return ErrUnavailable
	}
	if private && (st.Uid != uint32(os.Geteuid()) || st.Mode&07777 != 0700) {
		return ErrUnavailable
	}
	if !private && st.Uid != 0 && st.Uid != uint32(os.Geteuid()) {
		return ErrUnavailable
	}
	return nil
}
func openTrustedDirectory(path string, finalPrivate bool) (*os.File, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if checkDirectoryFD(fd, false) != nil {
		unix.Close(fd)
		return nil, ErrUnavailable
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			unix.Close(fd)
			return nil, ErrUnavailable
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
		if checkDirectoryFD(fd, finalPrivate && i == len(parts)-1) != nil {
			unix.Close(fd)
			return nil, ErrUnavailable
		}
	}
	return os.NewFile(uintptr(fd), path), nil
}
func openPrivateDirectory(path string) (*os.File, error) { return openTrustedDirectory(path, true) }

func readKey(path string) ([32]byte, error) {
	var key [32]byte
	parent, err := openPrivateDirectory(filepath.Dir(path))
	if err != nil {
		return key, err
	}
	defer parent.Close()
	name := filepath.Base(path)
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return key, err
	}
	f := os.NewFile(uintptr(fd), "snapshot history key")
	defer f.Close()
	var before, after unix.Stat_t
	if unix.Fstat(fd, &before) != nil || !safeFile(&before) || !aclSafe(fd) {
		return key, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(f, 33))
	if err != nil || len(data) != len(key) || unix.Fstat(fd, &after) != nil || !safeFile(&after) || before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Mtim != after.Mtim || !aclSafe(fd) {
		return key, ErrUnavailable
	}
	copy(key[:], data)
	return key, nil
}
func safeFile(st *unix.Stat_t) bool {
	return st.Mode&unix.S_IFMT == unix.S_IFREG && st.Mode&07777 == 0600 && st.Uid == uint32(os.Geteuid()) && st.Nlink == 1
}

func (s *store) check() error {
	if s.closed || s.failed || checkDirectory(s.root, true) != nil {
		return ErrUnavailable
	}
	current, err := openPrivateDirectory(s.rootPath)
	if err != nil {
		return ErrUnavailable
	}
	defer current.Close()
	var held, now unix.Stat_t
	if unix.Fstat(int(s.root.Fd()), &held) != nil || unix.Fstat(int(current.Fd()), &now) != nil || held.Dev != now.Dev || held.Ino != now.Ino {
		return ErrUnavailable
	}
	return nil
}

func (s *store) verifyIdentity() error {
	data, err := s.readFile(identityName, 4096)
	if err != nil {
		return ErrUnavailable
	}
	plain, err := openSealed(s.key, s.storeID, ObjectID{}, "identity", data)
	if err != nil {
		return ErrUnavailable
	}
	var got identity
	if json.Unmarshal(plain, &got) != nil || got.Version != payloadVersion || got.StoreID != hex.EncodeToString(s.storeID[:]) {
		return ErrUnavailable
	}
	canonical, err := json.Marshal(got)
	if err != nil || !bytes.Equal(plain, canonical) {
		return ErrUnavailable
	}
	return nil
}

func (s *store) scan(ctx context.Context, want *Intent) (usage, error) {
	return s.scanWithSummary(ctx, want, nil)
}

func (s *store) scanWithSummary(ctx context.Context, want *Intent, rebuilding *attendedSummary) (usage, error) {
	if err := s.check(); err != nil {
		return usage{}, err
	}
	names, err := directoryNames(s.root)
	if err != nil {
		return usage{}, err
	}
	seen := false
	u := usage{entries: len(names)}
	for _, name := range names {
		if ctx.Err() != nil {
			return usage{}, ctx.Err()
		}
		id, evidenceKind, isEvidence := parseEvidenceName(name)
		if name == identityName {
			seen = true
		} else if !(validObjectName(name) || validPendingName(name) || isEvidence || validEvidencePending(name)) {
			return usage{}, ErrUnavailable
		}
		fd, err := unix.Openat(int(s.root.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			return usage{}, ErrUnavailable
		}
		var st unix.Stat_t
		ok := unix.Fstat(fd, &st) == nil && safeFile(&st) && aclSafe(fd) && st.Size >= 0
		unix.Close(fd)
		if !ok || st.Size > int64(^uint64(0)>>1)-u.bytes {
			return usage{}, ErrUnavailable
		}
		u.bytes += st.Size
		if validObjectName(name) || validPendingName(name) {
			if u.operations == int(^uint(0)>>1) {
				return usage{}, ErrUnavailable
			}
			u.operations++
		}
		if isEvidence && s.checkEvidence(ctx, id, evidenceKind) != nil {
			return usage{}, ErrUnavailable
		}
		if id, ok := parseObjectName(name); ok {
			record, err := s.loadContext(ctx, id)
			if err != nil {
				return usage{}, ErrUnavailable
			}
			matches := want != nil && want.Attended != nil && record.Intent.Attended != nil && want.Attended.Stack == record.Intent.Attended.Stack &&
				subtle.ConstantTimeCompare(record.Intent.ApprovedTargetScope, want.ApprovedTargetScope) == 1
			if record.Intent.Attended != nil && (matches || rebuilding != nil) {
				terminal, err := s.attendedTerminal(id, record.Intent)
				if err != nil {
					return usage{}, err
				}
				if !terminal {
					u.unresolved = u.unresolved || matches
					if rebuilding != nil && !rebuilding.add(id, operationKey(record.Intent.ApprovedTargetScope, record.Intent.Attended.Stack)) {
						return usage{}, ErrUnavailable
					}
				}
			}
			if want != nil && subtle.ConstantTimeCompare(record.Intent.OriginalJournal, want.OriginalJournal) == 1 &&
				(subtle.ConstantTimeCompare(record.Intent.AttemptID, want.AttemptID) == 1 || subtle.ConstantTimeCompare(record.Intent.ProviderRequestID, want.ProviderRequestID) == 1) {
				u.duplicate = true
			}
		}
	}
	if !seen || ctx.Err() != nil {
		return usage{}, ErrUnavailable
	}
	return u, nil
}
func directoryNames(root *os.File) ([]string, error) {
	fd, err := unix.Openat(int(root.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "snapshot history directory")
	defer f.Close()
	names := make([]string, 0, directoryBatchEntries)
	for {
		batch, err := f.Readdirnames(directoryBatchEntries)
		if len(batch) > maxArchiveEntries-len(names) {
			return nil, ErrUnavailable
		}
		names = append(names, batch...)
		if errors.Is(err, io.EOF) {
			return names, nil
		}
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			return nil, ErrUnavailable
		}
	}
}
func validObjectName(name string) bool { _, ok := parseObjectName(name); return ok }
func parseObjectName(name string) (ObjectID, bool) {
	var id ObjectID
	if !strings.HasPrefix(name, objectPrefix) || len(name) != len(objectPrefix)+64 {
		return id, false
	}
	b, err := hex.DecodeString(strings.TrimPrefix(name, objectPrefix))
	if err != nil || len(b) != len(id) {
		return id, false
	}
	copy(id[:], b)
	return id, true
}
func validPendingName(name string) bool {
	if !strings.HasPrefix(name, pendingPrefix) || len(name) != len(pendingPrefix)+32 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(name, pendingPrefix))
	return err == nil
}
func objectName(id ObjectID) string { return objectPrefix + hex.EncodeToString(id[:]) }

func (s *store) commit(ctx context.Context, r CommitRequest) (OpenRequest, error) {
	if ctx == nil || ctx.Err() != nil {
		return OpenRequest{}, ErrUnavailable
	}
	digest, err := validateRequest(ctx, r)
	if err != nil {
		return OpenRequest{}, ErrUnavailable
	}
	p := payload{Version: payloadVersion, ObjectID: hex.EncodeToString(r.ObjectID[:]), AttemptID: append([]byte(nil), r.Intent.AttemptID...), ProviderRequestID: append([]byte(nil), r.Intent.ProviderRequestID...), OriginalJournal: append([]byte(nil), r.Intent.OriginalJournal...), ApprovedTargetScope: append([]byte(nil), r.Intent.ApprovedTargetScope...), Attended: cloneAttended(r.Intent.Attended), ManifestDigest: hex.EncodeToString(digest[:]), Snapshot: cloneSnapshot(r.Snapshot)}
	plain, err := json.Marshal(p)
	if err != nil {
		return OpenRequest{}, ErrUnavailable
	}
	// seal writes a 5-byte magic/version header, a standard 12-byte GCM nonce,
	// and the 16-byte GCM tag in addition to the canonical payload.
	sealedBytes := 5 + 12 + len(plain) + 16
	if sealedBytes < 0 || sealedBytes > maxObjectCiphertextBytes {
		return OpenRequest{}, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed || s.closed || s.check() != nil {
		return OpenRequest{}, ErrUnavailable
	}
	u, err := s.scan(ctx, &r.Intent)
	if err != nil {
		if ctx.Err() == nil {
			s.failed = true
		}
		return OpenRequest{}, ErrUnavailable
	}
	// Reserve one directory entry before creating the temporary object. Its
	// rename replaces that name rather than allocating a second entry.
	if u.entries >= maxArchiveEntries {
		return OpenRequest{}, ErrCapacity
	}
	if u.bytes > s.maxBytes-int64(sealedBytes) || u.operations >= s.maxOperations || s.reservedBytes > s.maxBytes-u.bytes-int64(sealedBytes) || s.reservedOperations >= s.maxOperations-u.operations {
		return OpenRequest{}, ErrCapacity
	}
	if _, err := s.readFile(objectName(r.ObjectID), maxObjectCiphertextBytes); err == nil {
		return OpenRequest{}, ErrExists
	} else if !errors.Is(err, os.ErrNotExist) {
		s.failed = true
		return OpenRequest{}, ErrUnavailable
	}
	if u.duplicate {
		return OpenRequest{}, ErrDuplicateIntent
	}
	var scopeStack attendedKey
	if r.Intent.Attended != nil {
		scopeStack = operationKey(r.Intent.ApprovedTargetScope, r.Intent.Attended.Stack)
		if s.attended == nil || (s.attended.counts[scopeStack] != 0) != u.unresolved {
			s.failed = true
			return OpenRequest{}, ErrUnavailable
		}
	}
	if u.unresolved {
		return OpenRequest{}, ErrUnresolvedOperation
	}
	s.reservedBytes += int64(sealedBytes)
	s.reservedOperations++
	defer func() { s.reservedBytes -= int64(sealedBytes); s.reservedOperations-- }()
	if ctx.Err() != nil {
		return OpenRequest{}, ErrUnavailable
	}
	sealed, err := seal(s.key, s.storeID, r.ObjectID, "object", plain)
	if err != nil || len(sealed) != sealedBytes {
		s.failed = true
		return OpenRequest{}, ErrUnavailable
	}
	if ctx.Err() != nil {
		return OpenRequest{}, ErrUnavailable
	}
	if err := writeNoClobber(s.root, objectName(r.ObjectID), sealed); err != nil {
		if errors.Is(err, ErrIndeterminate) {
			s.failed = true
		}
		return OpenRequest{}, err
	}
	if r.Intent.Attended != nil && !s.attended.add(r.ObjectID, scopeStack) {
		s.failed = true
		return OpenRequest{}, ErrIndeterminate
	}
	return OpenRequest{ObjectID: r.ObjectID, Intent: cloneIntent(r.Intent), ManifestDigest: digest}, nil
}

func validateRequest(ctx context.Context, r CommitRequest) ([32]byte, error) {
	var zero [32]byte
	if r.ObjectID == (ObjectID{}) || !validIntent(r.Intent) {
		return zero, ErrUnavailable
	}
	d, err := dillserve.SnapshotManifestDigest(ctx, r.Snapshot)
	if err != nil || subtle.ConstantTimeCompare(d[:], r.ExpectedManifestDigest[:]) != 1 {
		return zero, ErrUnavailable
	}
	return d, nil
}
func validIntent(i Intent) bool {
	return len(i.AttemptID) > 0 && len(i.AttemptID) <= 512 && len(i.ProviderRequestID) > 0 && len(i.ProviderRequestID) <= 512 && subtle.ConstantTimeCompare(i.AttemptID, i.ProviderRequestID) != 1 && len(i.OriginalJournal) > 0 && len(i.OriginalJournal) <= maxIntentBytes && len(i.ApprovedTargetScope) > 0 && len(i.ApprovedTargetScope) <= maxIntentBytes && validAttended(i.Attended)
}

func validAttended(a *AttendedIntent) bool {
	if a == nil {
		return true
	}
	return canonicalHex(a.Target, 32) && canonicalHex(a.AuthorityRevision, 32) &&
		len(a.Stack) > 0 && len(a.Stack) <= 128 && (a.Mode == "create" || a.Mode == "update") &&
		(a.PriorObject == "" || canonicalHex(a.PriorObject, 32)) && canonicalHex(a.Stage, 16) && strings.HasPrefix(a.Selection, "sha256:") && canonicalHex(strings.TrimPrefix(a.Selection, "sha256:"), 32)
}

func canonicalHex(s string, n int) bool {
	if len(s) != n*2 {
		return false
	}
	b, err := hex.DecodeString(s)
	return err == nil && hex.EncodeToString(b) == s
}

func cloneAttended(a *AttendedIntent) *AttendedIntent {
	if a == nil {
		return nil
	}
	out := *a
	return &out
}

func equalAttended(a, b *AttendedIntent) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
func cloneIntent(i Intent) Intent {
	return Intent{append([]byte(nil), i.AttemptID...), append([]byte(nil), i.ProviderRequestID...), append([]byte(nil), i.OriginalJournal...), append([]byte(nil), i.ApprovedTargetScope...), cloneAttended(i.Attended)}
}

func cloneSnapshot(in projectsnapshot.Snapshot) projectsnapshot.Snapshot {
	out := projectsnapshot.Snapshot{Entrypoint: in.Entrypoint, Files: make([]projectsnapshot.File, len(in.Files))}
	for n, f := range in.Files {
		out.Files[n] = projectsnapshot.File{Path: f.Path, Content: append([]byte(nil), f.Content...), SHA256: f.SHA256}
	}
	return out
}

func (s *store) resolveAttendedBinding(ctx context.Context, q AttendedBindingSelector) (OpenRequest, error) {
	if ctx.Err() != nil || s.check() != nil {
		return OpenRequest{}, ErrUnavailable
	}
	record, err := s.loadContext(ctx, q.Locator)
	if err != nil || ctx.Err() != nil {
		return OpenRequest{}, ErrUnavailable
	}
	a := record.Intent.Attended
	if a == nil || a.Target != q.Target || a.AuthorityRevision != q.AuthorityRevision || a.Stack != q.Stack ||
		subtle.ConstantTimeCompare(record.Intent.ApprovedTargetScope, []byte(q.Scope)) != 1 {
		return OpenRequest{}, ErrUnavailable
	}
	return OpenRequest{ObjectID: record.ObjectID, Intent: record.Intent, ManifestDigest: record.ManifestDigest}, nil
}

func (s *store) open(ctx context.Context, r OpenRequest) (Record, error) {
	if ctx == nil || ctx.Err() != nil || r.ObjectID == (ObjectID{}) || !validIntent(r.Intent) {
		return Record{}, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.check() != nil {
		return Record{}, ErrUnavailable
	}
	got, err := s.loadContext(ctx, r.ObjectID)
	if err != nil {
		return Record{}, ErrUnavailable
	}
	if ctx.Err() != nil || !equalAttended(got.Intent.Attended, r.Intent.Attended) || subtle.ConstantTimeCompare(got.Intent.AttemptID, r.Intent.AttemptID) != 1 || subtle.ConstantTimeCompare(got.Intent.ProviderRequestID, r.Intent.ProviderRequestID) != 1 || subtle.ConstantTimeCompare(got.Intent.OriginalJournal, r.Intent.OriginalJournal) != 1 || subtle.ConstantTimeCompare(got.Intent.ApprovedTargetScope, r.Intent.ApprovedTargetScope) != 1 || subtle.ConstantTimeCompare(got.ManifestDigest[:], r.ManifestDigest[:]) != 1 {
		return Record{}, ErrUnavailable
	}
	return got, nil
}

// The continuation authenticates the store and exact local scope and hides the
// scan position: the last examined object may belong to a different scope.
func (s *store) restartPosition(scope []byte, token string) (ObjectID, error) {
	var after ObjectID
	if token == "" {
		return after, nil
	}
	if len(token) != base64.RawURLEncoding.EncodedLen(restartCursorBytes) {
		return after, ErrUnavailable
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(data) != restartCursorBytes {
		return after, ErrUnavailable
	}
	plain, err := openSealed(s.key, s.storeID, ObjectID{}, "restart-cursor", data)
	binding := sha256.Sum256(scope)
	if err != nil || len(plain) != 2*sha256.Size || subtle.ConstantTimeCompare(plain[:sha256.Size], binding[:]) != 1 {
		return after, ErrUnavailable
	}
	copy(after[:], plain[sha256.Size:])
	return after, nil
}

func (s *store) discoverRestartMetadata(ctx context.Context, r RestartDiscoveryRequest, maxCiphertext int64) (RestartDiscoveryPage, error) {
	if ctx.Err() != nil || s.check() != nil {
		return RestartDiscoveryPage{}, ErrUnavailable
	}
	scope := r.ApprovedTargetScope
	after, err := s.restartPosition(scope, r.After)
	if err != nil {
		return RestartDiscoveryPage{}, ErrUnavailable
	}
	names, err := directoryNames(s.root)
	if err != nil {
		return RestartDiscoveryPage{}, ErrUnavailable
	}
	sort.Strings(names)
	var page RestartDiscoveryPage
	var used int64
	last := after
	examined := 0
	for _, name := range names {
		id, ok := parseObjectName(name)
		if !ok || bytes.Compare(id[:], after[:]) <= 0 {
			continue
		}
		if ctx.Err() != nil {
			return RestartDiscoveryPage{}, ErrUnavailable
		}
		if examined == maxRestartDiscoveryObjects || len(page.Records) == r.Limit {
			page.More = true
			break
		}
		size, err := s.sealedSize(name)
		if err != nil {
			return RestartDiscoveryPage{}, ErrUnavailable
		}
		if size > maxCiphertext-used {
			page.More = true
			break
		}
		record, err := s.loadContext(ctx, id)
		if err != nil {
			return RestartDiscoveryPage{}, ErrUnavailable
		}
		used += size
		examined++
		last = id
		if subtle.ConstantTimeCompare(record.Intent.ApprovedTargetScope, scope) != 1 {
			continue
		}
		page.Records = append(page.Records, RestartMetadata{Locator: id, AttemptID: record.Intent.AttemptID, ProviderRequestID: record.Intent.ProviderRequestID, OriginalJournal: record.Intent.OriginalJournal, ManifestDigest: record.ManifestDigest})
	}
	if page.More {
		if last == after {
			return RestartDiscoveryPage{}, ErrUnavailable
		}
		binding := sha256.Sum256(scope)
		plain := append(binding[:], last[:]...)
		data, err := seal(s.key, s.storeID, ObjectID{}, "restart-cursor", plain)
		if err != nil {
			return RestartDiscoveryPage{}, ErrUnavailable
		}
		page.Next = base64.RawURLEncoding.EncodeToString(data)
	}
	if ctx.Err() != nil {
		return RestartDiscoveryPage{}, ErrUnavailable
	}
	return page, nil
}
func (s *store) loadContext(ctx context.Context, id ObjectID) (Record, error) {
	if ctx == nil || ctx.Err() != nil {
		return Record{}, ErrUnavailable
	}
	data, err := s.readFile(objectName(id), maxObjectCiphertextBytes)
	if err != nil {
		return Record{}, err
	}
	plain, err := openSealed(s.key, s.storeID, id, "object", data)
	if err != nil {
		return Record{}, ErrUnavailable
	}
	var p payload
	if json.Unmarshal(plain, &p) != nil || p.Version != payloadVersion || p.ObjectID != hex.EncodeToString(id[:]) {
		return Record{}, ErrUnavailable
	}
	canonical, err := json.Marshal(p)
	if err != nil || !bytes.Equal(plain, canonical) {
		return Record{}, ErrUnavailable
	}
	var digest [32]byte
	b, err := hex.DecodeString(p.ManifestDigest)
	if err != nil || len(b) != 32 {
		return Record{}, ErrUnavailable
	}
	copy(digest[:], b)
	i := Intent{p.AttemptID, p.ProviderRequestID, p.OriginalJournal, p.ApprovedTargetScope, p.Attended}
	if !validIntent(i) {
		return Record{}, ErrUnavailable
	}
	computed, err := dillserve.SnapshotManifestDigest(ctx, p.Snapshot)
	if err != nil || computed != digest {
		return Record{}, ErrUnavailable
	}
	return Record{ObjectID: id, Intent: cloneIntent(i), ManifestDigest: digest, Snapshot: cloneSnapshot(p.Snapshot)}, nil
}
func (s *store) sealedSize(name string) (int64, error) {
	fd, err := unix.Openat(int(s.root.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, ErrUnavailable
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || !safeFile(&st) || !aclSafe(fd) || st.Size < 0 || st.Size > maxObjectCiphertextBytes {
		return 0, ErrUnavailable
	}
	return st.Size, nil
}

func (s *store) readFile(name string, limit int64) ([]byte, error) {
	fd, err := unix.Openat(int(s.root.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if err == unix.ENOENT {
			return nil, os.ErrNotExist
		}
		return nil, ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var before, after unix.Stat_t
	if unix.Fstat(fd, &before) != nil || !safeFile(&before) || !aclSafe(fd) || before.Size > limit {
		return nil, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit || unix.Fstat(fd, &after) != nil || !safeFile(&after) || before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Mtim != after.Mtim || !aclSafe(fd) {
		return nil, ErrUnavailable
	}
	return data, nil
}

func derive(key [32]byte, storeID [32]byte, id ObjectID, kind string) ([]byte, error) {
	info := append([]byte("brine.snapshot-history/aes-256-gcm/"+kind+"/v1\x00"), storeID[:]...)
	info = append(info, id[:]...)
	out := make([]byte, 32)
	_, err := io.ReadFull(hkdf.New(sha256.New, key[:], nil, info), out)
	return out, err
}
func seal(key [32]byte, storeID [32]byte, id ObjectID, kind string, plain []byte) ([]byte, error) {
	material, err := derive(key, storeID, id, kind)
	if err != nil {
		return nil, err
	}
	defer clear(material)
	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	aad := append([]byte("brine.snapshot-history/aad/"+kind+"/v1\x00"), storeID[:]...)
	aad = append(aad, id[:]...)
	out := append([]byte{'B', 'S', 'H', '1', formatVersion}, nonce...)
	return g.Seal(out, nonce, plain, aad), nil
}
func openSealed(key [32]byte, storeID [32]byte, id ObjectID, kind string, data []byte) ([]byte, error) {
	material, err := derive(key, storeID, id, kind)
	if err != nil {
		return nil, err
	}
	defer clear(material)
	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil || len(data) < 5+g.NonceSize()+g.Overhead() || !bytes.Equal(data[:4], []byte("BSH1")) || data[4] != formatVersion {
		return nil, ErrUnavailable
	}
	aad := append([]byte("brine.snapshot-history/aad/"+kind+"/v1\x00"), storeID[:]...)
	aad = append(aad, id[:]...)
	return g.Open(nil, data[5:5+g.NonceSize()], data[5+g.NonceSize():], aad)
}
func clear(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func writeNoClobber(root *os.File, name string, data []byte) error {
	return writeWithPendingPrefix(root, name, data, pendingPrefix)
}

func writeWithPendingPrefix(root *os.File, name string, data []byte, pending string) error {
	var random [16]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return ErrUnavailable
	}
	temp := pending + hex.EncodeToString(random[:])
	fd, err := unix.Openat(int(root.Fd()), temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), temp)
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || !safeFile(&st) || !aclSafe(fd) {
		return ErrUnavailable
	}
	if _, err := f.Write(data); err != nil {
		return ErrUnavailable
	}
	if testFault != nil {
		if err := testFault("before-file-sync"); err != nil {
			return ErrUnavailable
		}
	}
	if err := f.Sync(); err != nil {
		return ErrUnavailable
	}
	if err := f.Close(); err != nil {
		return ErrUnavailable
	}
	if testFault != nil {
		if err := testFault("before-rename"); err != nil {
			return ErrUnavailable
		}
	}
	// renameat2(RENAME_NOREPLACE) is the only publication primitive accepted by
	// v1. It moves one name atomically, avoiding a crash window with two links.
	// ENOSYS/EINVAL/EOPNOTSUPP/EXDEV and every other unsupported filesystem or
	// syscall result fail closed; there is intentionally no link/unlink fallback.
	if err := unix.Renameat2(int(root.Fd()), temp, int(root.Fd()), name, unix.RENAME_NOREPLACE); err != nil {
		if err == unix.EEXIST {
			return ErrExists
		}
		return ErrUnavailable
	}
	if testFault != nil {
		if err := testFault("after-rename-before-directory-sync"); err != nil {
			return ErrIndeterminate
		}
	}
	if testDirectorySync != nil {
		if err := testDirectorySync(int(root.Fd())); err != nil {
			return ErrIndeterminate
		}
	} else if err := unix.Fsync(int(root.Fd())); err != nil {
		return ErrIndeterminate
	}
	return nil
}
func (s *store) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.attended = nil
	clear(s.key[:])
	err := s.root.Close()
	return err
}
