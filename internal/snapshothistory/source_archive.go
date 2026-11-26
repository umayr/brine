package snapshothistory

import (
	"context"
	"encoding/hex"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

// SourceRevision is a private Brine workspace checkpoint, not a deployment,
// reviewed transfer, provider journal or request receipt.
type SourceRevision struct {
	Parent, RestoredFrom, Actor, Entrypoint string
	Created                                 time.Time
	Files                                   map[string][]byte
}

const (
	DefaultSourceArchiveObjects = 100
	DefaultSourceArchiveBytes   = 256 << 20

	// Hard read/validation bounds, independent of the operator's write policy.
	MaxSourceArchiveObjects = 512
	MaxSourceArchiveBytes   = 512 << 20
)

type SourceArchiveLimits struct {
	Objects int
	Bytes   int64
}

func (l SourceArchiveLimits) Resolve() (SourceArchiveLimits, error) {
	if l.Objects == 0 {
		l.Objects = DefaultSourceArchiveObjects
	}
	if l.Bytes == 0 {
		l.Bytes = DefaultSourceArchiveBytes
	}
	if l.Objects < 1 || l.Objects > MaxSourceArchiveObjects || l.Bytes < 1 || l.Bytes > MaxSourceArchiveBytes {
		return SourceArchiveLimits{}, ErrCapacity
	}
	return l, nil
}

type SourceArchiveUsage struct {
	Objects int
	Bytes   int64
	Limits  SourceArchiveLimits
}

func (u SourceArchiveUsage) RemainingObjects() int { return max(0, u.Limits.Objects-u.Objects) }
func (u SourceArchiveUsage) RemainingBytes() int64 { return max(0, u.Limits.Bytes-u.Bytes) }

type SourceArchive struct{ storage *sourceArchive }

func InitializeSourceArchive(dir, key, workspace string) error {
	return initSource(dir, key, workspace)
}
func OpenSourceArchive(dir, key, workspace string, limits SourceArchiveLimits) (*SourceArchive, error) {
	l, err := limits.Resolve()
	if err != nil {
		return nil, err
	}
	s, e := openSource(dir, key, workspace, l)
	if e != nil {
		return nil, e
	}
	return &SourceArchive{s}, nil
}
func (s *SourceArchive) Close() error { return s.storage.close() }

// Usage checks archive metadata and reports logical sealed bytes, not disk allocation.
// It does not attest that every revision is readable; reads and writes verify content.
func (s *SourceArchive) Usage(ctx context.Context) (SourceArchiveUsage, error) {
	return s.storage.usage(ctx)
}
func (s *SourceArchive) PutChain(ctx context.Context, scope string, ancestors []string, records []SourceRevision) ([]string, error) {
	return s.storage.putChain(ctx, scope, ancestors, records)
}
func (s *SourceArchive) Get(scope, id string) (SourceRevision, error) {
	return s.storage.get(scope, id)
}
func validSourceRevision(r SourceRevision) bool {
	for _, id := range []string{r.Parent, r.RestoredFrom} {
		if id != "" {
			if len(id) != 64 || strings.ToLower(id) != id {
				return false
			}
			if _, e := hex.DecodeString(id); e != nil {
				return false
			}
		}
	}
	if r.Actor == "" || len(r.Actor) > 128 || !utf8.ValidString(r.Actor) || strings.ContainsAny(r.Actor, "\x00\r\n") || r.Created.IsZero() || !strings.HasSuffix(r.Entrypoint, ".pkl") || strings.Contains(r.Entrypoint, "/") || len(r.Files) == 0 || len(r.Files) > 128 {
		return false
	}
	if _, ok := r.Files[r.Entrypoint]; !ok {
		return false
	}
	total := 0
	for p, b := range r.Files {
		if !utf8.ValidString(p) || path.Clean(p) != p || len(p) > 1024 || strings.ContainsAny(p, "\\\x00\r\n") || (!strings.HasSuffix(p, ".pkl") && p != "PklProject") {
			return false
		}
		parts := strings.Split(p, "/")
		if len(parts) > 16 {
			return false
		}
		for _, part := range parts {
			if part == "" || strings.HasPrefix(part, ".") || len(part) > 128 {
				return false
			}
		}
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			if _, ok := r.Files[d]; ok {
				return false
			}
		}
		if len(b) > (4<<20)-total {
			return false
		}
		total += len(b)
	}
	return true
}
