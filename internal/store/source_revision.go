package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

var (
	ErrSourceUnavailable = errors.New("source revisions unavailable for this workspace")
	ErrSourceInterrupted = errors.New("source publication interrupted: preserve both directories and reconcile offline before further changes")
	ErrSourceStale       = errors.New("source changed since review; nothing restored")
	ErrSourceUncertain   = errors.New("source revision outcome unknown; inspect the current source and history, do not repeat this write")
)

const MaxSourceRevisions = 100
const maxSourceSize = 4 << 20

var sourceComponent = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._ -]{0,127}$`)

// An ID is opaque, never an authorization. Only reachable revisions belong to
// this registration. Restores append; they cannot move the head backwards.
type SourceLineage struct {
	Identity  string   `json:"identity"`
	Revisions []string `json:"revisions"`
}

func hexID(id string) bool {
	if len(id) != 64 || id != strings.ToLower(id) {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
func (l *SourceLineage) valid() bool {
	if l == nil {
		return true
	}
	if !hexID(l.Identity) || len(l.Revisions) == 0 || len(l.Revisions) > MaxSourceRevisions {
		return false
	}
	seen := map[string]bool{}
	for _, id := range l.Revisions {
		if !hexID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
func revisionScope(stack *Stack, id string) string {
	b, _ := json.Marshal([]string{"brine/workspace-source/v1", stack.Name, stack.CreatedAt.UTC().Format(time.RFC3339Nano), id})
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

type SourceView struct {
	Scope, Baseline, Entrypoint string
	Revisions                   []string
	Files                       map[string][]byte
}
type SourceUpdate struct {
	NewRevisions []string
	Entrypoint   string
	Files        map[string][]byte
} // nil Files: retain only, do not rewrite source.
type SourceChange func(SourceView) (SourceUpdate, error)
type RevisionWorkspace interface {
	ReviewSource(context.Context, string) (SourceView, error)
	CommitSource(context.Context, string, string, SourceChange) error
}

func sourcePathOK(p string, dir bool) bool {
	if !path.IsAbs(p) && path.Clean(p) == p && len(p) <= 1024 {
		parts := strings.Split(p, "/")
		if len(parts) > 16 {
			return false
		}
		for _, s := range parts {
			if !sourceComponent.MatchString(s) {
				return false
			}
		}
		return dir || strings.HasSuffix(p, ".pkl") || p == "PklProject"
	}
	return false
}
func validSourceFiles(files map[string][]byte, entry string) bool {
	if len(files) == 0 || len(files) > 128 || !sourcePathOK(entry, false) || strings.Contains(entry, "/") || !strings.HasSuffix(entry, ".pkl") {
		return false
	}
	if _, ok := files[entry]; !ok {
		return false
	}
	size := 0
	for p, b := range files {
		if !sourcePathOK(p, false) {
			return false
		}
		size += len(b)
		if size > maxSourceSize {
			return false
		}
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			if _, ok := files[d]; ok {
				return false
			}
		}
	}
	return true
}
func sourceBaseline(st *Stack, identity string, files map[string][]byte) string {
	b, _ := json.Marshal([]any{"source-review/v1", st, identity, files})
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func newLineage() (*SourceLineage, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, ErrSourceUnavailable
	}
	return &SourceLineage{Identity: hex.EncodeToString(b[:])}, nil
}
