package web

import (
	"sort"
	"time"
)

type brokerHistoryRow struct {
	Target, Kind, Status, Effects, ID, Generation, Source, Exact string
	Registration                                                 string
	Revision                                                     int64
	At                                                           time.Time
	Count                                                        int
}

func (h *Handler) brokerHistory(stack string) ([]brokerHistoryRow, bool) {
	out := []brokerHistoryRow{}
	if h.broker == nil {
		return out, false
	}
	scopes, err := h.broker.State.ScopesForStack(stack)
	if err != nil {
		return nil, true
	}
	for _, scope := range scopes {
		audit, err := h.broker.State.History(scope)
		if err != nil {
			return nil, true
		}
		for _, e := range audit {
			out = append(out, brokerHistoryRow{Target: scope.TargetRegistration, Kind: "Binding references", Status: string(e.Kind), Revision: e.Revision, At: e.At, Exact: e.At.Format(time.RFC3339Nano), Count: e.BindingCount})
		}
		records, err := h.broker.State.Deployments(scope)
		if err != nil {
			return nil, true
		}
		for _, r := range records {
			last := r.Events[len(r.Events)-1]
			status := last.State
			if status == "pending" {
				status = "unknown"
			}
			kind := "Secret deployment"
			if r.Intent.Retirement != nil {
				kind = "Secret retirement"
			}
			out = append(out, brokerHistoryRow{Target: scope.TargetRegistration, Kind: kind, Status: status, Effects: last.Effects, ID: r.Intent.ID, Registration: scope.RegistrationID, Generation: r.Intent.Reference.Scope.Generation, Source: r.Intent.Identity.SourceDigest, Revision: r.Intent.Identity.BindingRevision, At: r.At, Exact: r.At.Format(time.RFC3339Nano)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, false
}
