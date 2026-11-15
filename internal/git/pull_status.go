package git

import (
	"context"
	"time"
)

// PullRunner is the Git functionality needed by a manual pull.
type PullRunner interface {
	Pull(context.Context, string) (bool, error)
	ShortRef(context.Context, string) (string, error)
}

// PullStatusStore persists the latest manual pull observation.
type PullStatusStore interface {
	UpdateGitStatus(name, commit, syncErr string, syncTime time.Time) error
}

// PullAndRecord runs a manual pull and records its outcome on a best-effort
// basis. The pull may already have changed the working tree, so a later status
// or short-ref failure must not be reported as a failed pull or invite a retry.
func PullAndRecord(ctx context.Context, runner PullRunner, status PullStatusStore, name, repoDir string) (changed bool, commit string, err error) {
	changed, err = runner.Pull(ctx, repoDir)
	if err != nil {
		_ = status.UpdateGitStatus(name, "", err.Error(), time.Now())
		return false, "", err
	}
	commit, _ = runner.ShortRef(ctx, repoDir)
	_ = status.UpdateGitStatus(name, commit, "", time.Now())
	return changed, commit, nil
}
