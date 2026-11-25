package snapshothistory

import (
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
type SourceArchive struct{ storage *sourceArchive }

func InitializeSourceArchive(dir, key, workspace string) error {
	return initSource(dir, key, workspace)
}
func OpenSourceArchive(dir, key, workspace string) (*SourceArchive, error) {
	s, e := openSource(dir, key, workspace)
	if e != nil {
		return nil, e
	}
	return &SourceArchive{s}, nil
}
func (s *SourceArchive) Close() error { return s.storage.close() }
func (s *SourceArchive) Put(scope string, r SourceRevision) (string, error) {
	return s.storage.put(scope, r)
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
