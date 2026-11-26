package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CreateGitAttempt stages a complete stack privately, then publishes it without
// replacing an existing path. clone must finish using its destination before returning.
// Once publication starts, cancellation cannot roll it back.
func (s *FSStore) CreateGitAttempt(ctx context.Context, name string, src *GitSource, entrypoint string, clone func(context.Context, string) error) (*Stack, error) {
	if err := s.sourceInterrupted(name); err != nil {
		return nil, err
	}
	if src == nil || clone == nil {
		return nil, fmt.Errorf("git source and clone callback are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp(filepath.Join(s.dataDir, "stacks"), ".git-attempt-")
	if err != nil {
		return nil, fmt.Errorf("create git staging: %w", err)
	}
	defer os.RemoveAll(root)
	staging, err := NewFSStore(root)
	if err != nil {
		return nil, err
	}
	repo, err := staging.RepoDir(name)
	if err != nil {
		return nil, err
	}
	if err := clone(ctx, repo); err != nil {
		return nil, fmt.Errorf("git clone: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stack, err := staging.CreateFromGit(name, src, entrypoint)
	if err != nil {
		return nil, err
	}
	// Store metadata is interpreted relative to the final store on read.
	stack.PklPath = filepath.Join(s.filesDir(name, src), entrypointOrDefault(entrypoint))
	if err := staging.writeMeta(stack); err != nil {
		return nil, fmt.Errorf("write staged metadata: %w", err)
	}
	// Use the existing global mutation lock, but do not let contention outlive
	// the attempt deadline. No waiter goroutine survives cancellation.
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.mu.TryLock() {
			break
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.sourceInterrupted(name); err != nil {
		return nil, err
	}
	if err := renameNoReplace(staging.stackDir(name), s.stackDir(name)); err != nil {
		return nil, fmt.Errorf("publish stack: %w", err)
	}
	return stack, nil
}
