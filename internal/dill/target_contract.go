package dill

import "github.com/umayr/brine/internal/target"

// TargetIdentity is configuration only, not negotiated engine identity/readiness.
func (r *Runner) TargetIdentity() target.Identity {
	return target.Configured(r.Profile, r.Engine, r.Socket)
}

// Legacy/secret controllers may never bypass the qualified operation adapter.
// The public operation entry points route configured requests before this gate.
// Metadata and secret discovery are not permission to mutate an Alpha target.
func (r *Runner) checkExecutionContract() error {
	if r.Profile == target.Alpha || r.QualifiedOperation != nil {
		return target.ErrNegotiationUnavailable
	}
	return nil
}
