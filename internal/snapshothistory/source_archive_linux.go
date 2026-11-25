//go:build linux

package snapshothistory

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	sourceIdentityName = ".brine-source-history.identity"
	sourceObjectPrefix = "source-object-"
	sourceIdentityKind = "source-history-identity"
	sourceObjectKind   = "source-history-revision"
	sourceMaxBytes     = 256 << 20
	sourceMaxObjects   = 100
)

type sourceArchive struct {
	s           *store
	objectStore [sha256.Size]byte
}

type sourcePayload struct {
	Scope    string         `json:"scope"`
	Revision SourceRevision `json:"revision"`
}

type sourceUsage struct {
	bytes   int64
	objects int
	found   sourcePayload
}

func lockSource(dir, key, workspace string) (*store, error) {
	options := Options{
		ArchiveDir:    dir,
		KeyFile:       key,
		WorkspaceDir:  workspace,
		StoreID:       sha256.Sum256([]byte("brine.source-history/path/v1\x00" + filepath.Clean(dir))),
		MaxBytes:      sourceMaxBytes,
		MaxOperations: sourceMaxObjects,
	}
	root, keyBytes, normalized, err := validateOptions(options)
	if err != nil {
		return nil, ErrUnavailable
	}
	s := &store{root: root, rootPath: normalized.ArchiveDir, key: keyBytes, storeID: normalized.StoreID, maxBytes: normalized.MaxBytes, maxOperations: normalized.MaxOperations}
	if unix.Flock(int(root.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil || s.check() != nil {
		s.close()
		return nil, ErrUnavailable
	}
	return s, nil
}

func initSource(dir, key, workspace string) error {
	s, err := lockSource(dir, key, workspace)
	if err != nil {
		return err
	}
	defer s.close()
	names, err := directoryNames(s.root)
	if err != nil || len(names) != 0 {
		return ErrUnavailable
	}
	// A new generation prevents an old object from opening after reinitializing
	// this path with the same key. The identity itself is bound to the path.
	var generation [sha256.Size]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return ErrUnavailable
	}
	sealed, err := seal(s.key, s.storeID, ObjectID{}, sourceIdentityKind, generation[:])
	if err != nil {
		return ErrUnavailable
	}
	return writeNoClobber(s.root, sourceIdentityName, sealed)
}

func openSource(dir, key, workspace string) (*sourceArchive, error) {
	s, err := lockSource(dir, key, workspace)
	if err != nil {
		return nil, err
	}
	objectStore, _, err := readSourceIdentity(s)
	if err != nil {
		s.close()
		return nil, err
	}
	a := &sourceArchive{s: s, objectStore: objectStore}
	// Validate existing entries and sync any publication that was visible but
	// indeterminate after a prior rename.
	if _, err := a.scan(""); err != nil || unix.Fsync(int(s.root.Fd())) != nil {
		s.close()
		return nil, ErrUnavailable
	}
	return a, nil
}

func readSourceIdentity(s *store) ([sha256.Size]byte, int64, error) {
	sealed, err := s.readFile(sourceIdentityName, 4096)
	if err != nil {
		return [sha256.Size]byte{}, 0, ErrUnavailable
	}
	plain, err := openSealed(s.key, s.storeID, ObjectID{}, sourceIdentityKind, sealed)
	if err != nil || len(plain) != sha256.Size {
		return [sha256.Size]byte{}, 0, ErrUnavailable
	}
	var binding [2 * sha256.Size]byte
	copy(binding[:sha256.Size], s.storeID[:])
	copy(binding[sha256.Size:], plain)
	return sha256.Sum256(binding[:]), int64(len(sealed)), nil
}

// scan runs with exclusive access (under the handle mutex once opened). Pending
// files can be partial writes: check their names and metadata, but never use
// their bytes as records. Charge every remnant to both quotas.
func (a *sourceArchive) scan(want string) (sourceUsage, error) {
	s := a.s
	if s.check() != nil {
		return sourceUsage{}, ErrUnavailable
	}
	objectStore, size, err := readSourceIdentity(s)
	if err != nil || objectStore != a.objectStore {
		return sourceUsage{}, ErrUnavailable
	}
	names, err := directoryNames(s.root)
	if err != nil || len(names) == 0 || len(names) > s.maxOperations+1 {
		return sourceUsage{}, ErrUnavailable
	}
	u := sourceUsage{bytes: size, objects: len(names) - 1}
	seenIdentity := false
	for _, name := range names {
		if name == sourceIdentityName {
			seenIdentity = true
			continue
		}
		if suffix, ok := strings.CutPrefix(name, pendingPrefix); ok && canonicalHex(suffix, 16) {
			n, err := s.sealedSize(name)
			if err != nil || n > s.maxBytes-u.bytes {
				return sourceUsage{}, ErrUnavailable
			}
			u.bytes += n
			continue
		}
		idText, ok := strings.CutPrefix(name, sourceObjectPrefix)
		if !ok || !canonicalHex(idText, sha256.Size) {
			return sourceUsage{}, ErrUnavailable
		}
		var id ObjectID
		if _, err := hex.Decode(id[:], []byte(idText)); err != nil {
			return sourceUsage{}, ErrUnavailable
		}
		limit := s.maxBytes - u.bytes
		if limit > maxObjectCiphertextBytes {
			limit = maxObjectCiphertextBytes
		}
		sealed, err := s.readFile(name, limit)
		if err != nil {
			return sourceUsage{}, ErrUnavailable
		}
		u.bytes += int64(len(sealed))
		plain, err := openSealed(s.key, a.objectStore, id, sourceObjectKind, sealed)
		var p sourcePayload
		if err != nil || json.Unmarshal(plain, &p) != nil || !canonicalHex(p.Scope, sha256.Size) || !validSourceRevision(p.Revision) {
			return sourceUsage{}, ErrUnavailable
		}
		p.Revision.Created = p.Revision.Created.UTC()
		canonical, err := json.Marshal(p)
		if err != nil || !bytes.Equal(plain, canonical) {
			return sourceUsage{}, ErrUnavailable
		}
		if idText == want {
			u.found = p
		}
	}
	if !seenIdentity {
		return sourceUsage{}, ErrUnavailable
	}
	return u, nil
}

func (a *sourceArchive) put(scope string, r SourceRevision) (string, error) {
	if !canonicalHex(scope, sha256.Size) || !validSourceRevision(r) {
		return "", ErrUnavailable
	}
	// JSON would silently replace invalid UTF-8 in strings, including file
	// paths. File contents are encoded as bytes, not strings.
	if !utf8.ValidString(r.Parent) || !utf8.ValidString(r.RestoredFrom) || !utf8.ValidString(r.Actor) || !utf8.ValidString(r.Entrypoint) {
		return "", ErrUnavailable
	}
	for path := range r.Files {
		if !utf8.ValidString(path) {
			return "", ErrUnavailable
		}
	}
	r.Created = r.Created.UTC()
	s := a.s
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := a.scan("")
	if err != nil {
		return "", err
	}
	if u.objects >= s.maxOperations {
		return "", ErrCapacity
	}
	plain, err := json.Marshal(sourcePayload{Scope: scope, Revision: r})
	if err != nil {
		return "", ErrUnavailable
	}
	var id ObjectID
	if _, err := rand.Read(id[:]); err != nil {
		return "", ErrUnavailable
	}
	sealed, err := seal(s.key, a.objectStore, id, sourceObjectKind, plain)
	if err != nil {
		return "", ErrUnavailable
	}
	if len(sealed) > maxObjectCiphertextBytes || int64(len(sealed)) > s.maxBytes-u.bytes {
		return "", ErrCapacity
	}
	idText := hex.EncodeToString(id[:])
	if err := writeNoClobber(s.root, sourceObjectPrefix+idText, sealed); err != nil {
		if errors.Is(err, ErrIndeterminate) {
			s.failed = true
		}
		return "", err
	}
	return idText, nil
}

func (a *sourceArchive) get(scope, id string) (SourceRevision, error) {
	if !canonicalHex(scope, sha256.Size) || !canonicalHex(id, sha256.Size) {
		return SourceRevision{}, ErrUnavailable
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	u, err := a.scan(id)
	if err != nil || u.found.Scope != scope {
		return SourceRevision{}, ErrUnavailable
	}
	return u.found.Revision, nil
}

func (a *sourceArchive) close() error { return a.s.close() }
