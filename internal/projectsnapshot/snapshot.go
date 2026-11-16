// Package projectsnapshot builds in-memory, bounded project snapshots. It does
// not identify secrets: callers must treat all captured bytes as sensitive.
package projectsnapshot

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path"
	"strings"
)

// Limits are strict, positive upper bounds. Entries counts files and directories,
// excluding the root and deliberately omitted .git entries. Depth counts path
// components, so a root-level file has depth 1.
type Limits struct {
	MaxFiles      int
	MaxEntries    int
	MaxDepth      int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

// File is a captured regular file, identified by its slash-separated project path.
type File struct {
	Path    string
	Content []byte
	SHA256  [sha256.Size]byte
}

// Snapshot is ordered lexicographically by Path. Entrypoint names one of Files.
type Snapshot struct {
	Entrypoint string
	Files      []File
}

// Build captures a project rooted at root. It never follows project symlinks and
// deliberately omits every entry named .git. It fails rather than returning a
// partial snapshot. Concurrent modifications are detected where practical, but
// this is not an atomic filesystem snapshot or a sandbox.
func Build(root, entrypoint string, limits Limits) (Snapshot, error) {
	if root == "" {
		return Snapshot{}, errors.New("empty project root")
	}
	if entrypoint == "" || strings.Contains(entrypoint, "\\") || (len(entrypoint) >= 2 && entrypoint[1] == ':') || strings.HasPrefix(entrypoint, "/") || path.Clean(entrypoint) != entrypoint || entrypoint == "." || entrypoint == ".." || strings.HasPrefix(entrypoint, "../") {
		return Snapshot{}, errors.New("entrypoint must be a canonical relative slash path")
	}
	for _, part := range strings.Split(entrypoint, "/") {
		if part == ".git" {
			return Snapshot{}, errors.New("entrypoint is excluded")
		}
	}
	if limits.MaxFiles <= 0 || limits.MaxEntries <= 0 || limits.MaxDepth <= 0 || limits.MaxFileBytes <= 0 || limits.MaxTotalBytes <= 0 || limits.MaxFileBytes == int64(^uint64(0)>>1) {
		return Snapshot{}, fmt.Errorf("all limits must be positive and MaxFileBytes must be below MaxInt64")
	}
	return build(root, entrypoint, limits)
}
