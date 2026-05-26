package git

import (
	"context"
	"io"
	"log"
	"time"

	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/store"
)

const defaultPollInterval = 5 * time.Minute
const minCheckInterval = 30 * time.Second

// Syncer watches all GitOps-enabled stacks and triggers dill up when
// upstream commits arrive.
type Syncer struct {
	store store.StackStore
	git   *Runner
	dill  *dill.Runner
}

// NewSyncer creates a Syncer.
func NewSyncer(st store.StackStore, git *Runner, dill *dill.Runner) *Syncer {
	return &Syncer{store: st, git: git, dill: dill}
}

// Run starts the background sync loop. It blocks until ctx is cancelled.
func (s *Syncer) Run(ctx context.Context) {
	ticker := time.NewTicker(minCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncAll(ctx)
		}
	}
}

func (s *Syncer) syncAll(ctx context.Context) {
	stacks, err := s.store.List()
	if err != nil {
		return
	}
	now := time.Now()
	for _, stack := range stacks {
		if stack.Git == nil || !stack.Git.AutoDeploy {
			continue
		}
		interval := parsePollInterval(stack.Git.PollInterval)
		if stack.Git.LastSyncAt != nil && now.Sub(*stack.Git.LastSyncAt) < interval {
			continue
		}
		// Run in a goroutine so one slow repo doesn't block others.
		go s.sync(ctx, stack.Name)
	}
}

func (s *Syncer) sync(ctx context.Context, name string) {
	stack, err := s.store.Get(name)
	if err != nil {
		return
	}
	repoDir, err := s.store.RepoDir(name)
	if err != nil {
		return
	}

	// For non-read-only stacks, skip the pull if the user has uncommitted
	// local edits — we don't want to clobber their work.
	if !stack.Git.ReadOnly {
		dirty, _ := s.git.HasChanges(ctx, repoDir)
		if dirty {
			syncTime := time.Now()
			_ = s.store.UpdateGitStatus(name, stack.Git.LastCommit, "skipped: uncommitted local changes", syncTime)
			return
		}
	}

	changed, err := s.git.Pull(ctx, repoDir)
	if err != nil {
		syncTime := time.Now()
		_ = s.store.UpdateGitStatus(name, "", err.Error(), syncTime)
		log.Printf("git syncer: pull %s: %v", name, err)
		return
	}

	commit, _ := s.git.ShortRef(ctx, repoDir)
	syncTime := time.Now()
	_ = s.store.UpdateGitStatus(name, commit, "", syncTime)

	if changed {
		log.Printf("git syncer: %s updated to %s, deploying", name, commit)
		if err := s.dill.Up(ctx, stack.PklPath, io.Discard); err != nil {
			log.Printf("git syncer: dill up %s: %v", name, err)
		}
	}
}

func parsePollInterval(s string) time.Duration {
	if s == "" {
		return defaultPollInterval
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < minCheckInterval {
		return defaultPollInterval
	}
	return d
}
