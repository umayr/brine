package brokerapp

import (
	"github.com/umayr/brine/internal/brokerwire"
	"github.com/umayr/brine/internal/secretbroker"
)

// References identify authenticated target evidence, never infer files or live
// outcomes from a receipt. Dill independently checks MAC, active state and live
// coverage; unresolved application requests prevent any new preparation.
func (c *Controller) retirementRequest(i secretbroker.ReviewIdentity, scope brokerwire.Scope, records []secretbroker.DeploymentRecord) (brokerwire.Retirement, error) {
	snapshot, err := c.service.State.Load(i.Scope)
	if err != nil || snapshot.Revision != i.BindingRevision || len(snapshot.Bindings) != 0 {
		return brokerwire.Retirement{}, ErrUnavailable
	}
	request := brokerwire.Retirement{Contract: brokerwire.RetirementVersion, Scope: scope}
	seen := map[string]bool{}
	for _, r := range records {
		if len(r.Events) == 0 {
			return request, ErrUnavailable
		}
		last := r.Events[len(r.Events)-1]
		if last.State != "succeeded" && !(last.State == "failed" && last.Effects == "partial") {
			continue
		}
		ref := r.Intent.Reference
		s := ref.Scope
		if s.Registration != scope.Registration || s.Stack != scope.Stack || s.Engine != scope.Engine || s.TargetDigest != scope.TargetDigest || s.EndpointDigest != scope.EndpointDigest {
			continue
		}
		if !seen[ref.Handle] {
			request.Previous = append(request.Previous, ref)
			seen[ref.Handle] = true
		}
	}
	if !request.Valid() {
		return brokerwire.Retirement{}, ErrUnavailable
	}
	return request, nil
}
