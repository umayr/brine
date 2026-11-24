package dill

import (
	"context"
	"errors"
	"github.com/umayr/brine/internal/secretbindings"
	"os/exec"
)

// ConfigureQuarantine runs before the Runner is shared with handlers.
func (r *Runner) ConfigureQuarantine(q *secretbindings.Quarantine) {
	r.quarantine = q
	if q != nil {
		r.Coordinator.CheckAdmission = q.Check
	}
}
func (r *Runner) runTracked(ctx context.Context, cmd *exec.Cmd) error {
	return r.runTrackedWith(ctx, cmd, runSupervised)
}

func (r *Runner) runTrackedWith(ctx context.Context, cmd *exec.Cmd, supervise func(context.Context, *exec.Cmd) error) error {
	if r.quarantine == nil {
		return supervise(ctx, cmd)
	}
	name, path := "", ""
	for i := 1; i+1 < len(cmd.Args); i++ {
		switch cmd.Args[i] {
		case "-p":
			name = cmd.Args[i+1]
		case "-f":
			path = cmd.Args[i+1]
		}
	}
	if name == "" && r.ResolveStack != nil {
		var err error
		name, err = r.ResolveStack(path)
		if err != nil {
			return err
		}
	}
	if name == "" {
		return errors.New("process quarantine requires trusted deployment identity")
	}
	id := secretbindings.DeploymentID(name)
	token, err := r.quarantine.Begin(id)
	if err != nil {
		return err
	}
	err = supervise(ctx, cmd)
	if errors.Is(err, ErrProcessUnknown) {
		return err
	}
	if completeErr := r.quarantine.Complete(id, token); completeErr != nil {
		return errors.Join(ErrProcessUnknown, err, completeErr)
	}
	return err
}
