//go:build linux

package snapshothistory

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

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
	links   map[string][2]string // authenticated parent and restore source for the requested scope
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
	if _, err := a.scan(context.Background(), ""); err != nil || unix.Fsync(int(s.root.Fd())) != nil {
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
func (a *sourceArchive) scan(ctx context.Context, scope string) (sourceUsage, error) {
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
	u := sourceUsage{bytes: size, objects: len(names) - 1, links: map[string][2]string{}}
	seenIdentity := false
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return sourceUsage{}, err
		}
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
		p, err := a.decode(id, sealed)
		if err != nil {
			return sourceUsage{}, err
		}
		if scope != "" && p.Scope == scope {
			u.links[idText] = [2]string{p.Revision.Parent, p.Revision.RestoredFrom}
		}
	}
	if !seenIdentity {
		return sourceUsage{}, ErrUnavailable
	}
	return u, nil
}

func (a *sourceArchive) decode(id ObjectID, sealed []byte) (sourcePayload, error) {
	plain, err := openSealed(a.s.key, a.objectStore, id, sourceObjectKind, sealed)
	var p sourcePayload
	if err != nil || json.Unmarshal(plain, &p) != nil || !canonicalHex(p.Scope, sha256.Size) || !validSourceRevision(p.Revision) {
		return sourcePayload{}, ErrUnavailable
	}
	p.Revision.Created = p.Revision.Created.UTC()
	canonical, err := json.Marshal(p)
	if err != nil || !bytes.Equal(plain, canonical) {
		return sourcePayload{}, ErrUnavailable
	}
	return p, nil
}

// Reserve count and byte capacity for the entire publication under one lock.
// An I/O failure may leave charged but unreachable objects; the workspace
// only adds reachability after receiving the complete durable set.
func (a *sourceArchive) putChain(ctx context.Context, scope string, ancestors []string, records []SourceRevision) ([]string, error) {
	if !canonicalHex(scope, sha256.Size) || len(records) < 1 || len(records) > 2 || len(ancestors) > sourceMaxObjects {
		return nil, ErrUnavailable
	}
	s := a.s
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := a.scan(ctx, scope)
	if err != nil {
		return nil, err
	}
	parent := ""
	seen := map[string]bool{}
	for _, id := range ancestors {
		link, ok := u.links[id]
		if !ok || seen[id] || link[0] != parent || (link[1] != "" && !seen[link[1]]) {
			return nil, ErrUnavailable
		}
		seen[id] = true
		parent = id
	}
	if u.objects+len(records) > s.maxOperations {
		return nil, ErrCapacity
	}
	ids := make([]string, 0, len(records))
	members := make([][]byte, 0, len(records))
	for i, r := range records {
		if i == 0 && r.Parent != parent || r.RestoredFrom != "" && !seen[r.RestoredFrom] {
			return nil, ErrUnavailable
		}
		r.Parent = parent
		r.Created = r.Created.UTC()
		if !validSourceRevision(r) {
			return nil, ErrUnavailable
		}
		var id ObjectID
		if _, err := rand.Read(id[:]); err != nil {
			return nil, ErrUnavailable
		}
		plain, err := json.Marshal(sourcePayload{Scope: scope, Revision: r})
		if err != nil {
			return nil, ErrUnavailable
		}
		sealed, err := seal(s.key, a.objectStore, id, sourceObjectKind, plain)
		if err != nil {
			return nil, ErrUnavailable
		}
		if len(sealed) > maxObjectCiphertextBytes || int64(len(sealed)) > s.maxBytes-u.bytes {
			return nil, ErrCapacity
		}
		u.bytes += int64(len(sealed))
		parent = hex.EncodeToString(id[:])
		ids = append(ids, parent)
		members = append(members, sealed)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for i, b := range members {
		if err := writeNoClobber(s.root, sourceObjectPrefix+ids[i], b); err != nil {
			if errors.Is(err, ErrIndeterminate) {
				s.failed = true
			}
			return nil, err
		}
	}
	return ids, nil
}

func (a *sourceArchive) get(scope, idText string) (SourceRevision, error) {
	if !canonicalHex(scope, sha256.Size) || !canonicalHex(idText, sha256.Size) {
		return SourceRevision{}, ErrUnavailable
	}
	s := a.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.check() != nil {
		return SourceRevision{}, ErrUnavailable
	}
	generation, _, err := readSourceIdentity(s)
	if err != nil || generation != a.objectStore {
		return SourceRevision{}, ErrUnavailable
	}
	var id ObjectID
	hex.Decode(id[:], []byte(idText))
	b, err := s.readFile(sourceObjectPrefix+idText, maxObjectCiphertextBytes)
	if err != nil {
		return SourceRevision{}, ErrUnavailable
	}
	p, err := a.decode(id, b)
	if err != nil || p.Scope != scope {
		return SourceRevision{}, ErrUnavailable
	}
	return p.Revision, nil
}

func (a *sourceArchive) close() error { return a.s.close() }
