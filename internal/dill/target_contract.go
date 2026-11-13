package dill

import "github.com/umayr/brine/internal/target"

// TargetIdentity is configuration only, not negotiated engine identity/readiness.
func (r *Runner) TargetIdentity() target.Identity {
	return target.Configured(r.Profile, r.Engine, r.Socket)
}

// No released general target execution contract exists. Never turn a metadata
// protocol.check or secret-capabilities result into permission to mutate.
func (r *Runner) checkExecutionContract() error {
	if r.Profile == target.Alpha {
		return target.ErrNegotiationUnavailable
	}
	return nil
}
