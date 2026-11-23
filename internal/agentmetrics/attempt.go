package agentmetrics

import (
	"errors"
	"sync"
)

// Sample carries qualified aggregate values only. Presence flags distinguish
// missing readings from measured zero. It is not a workload health claim.
type Sample = sample

// SampleAttempt is one local receipt slot for the candidate offline format.
// A zero value rejects all results. Do not copy an attempt after first use.
// It owns no transport, registry, scheduler, entitlement or display state.
type SampleAttempt struct {
	mu  sync.Mutex
	ctx Context
}

// NewSampleAttempt snapshots a caller-verified tuple. It neither establishes
// nor broadens authority. The caller must issue a unique request ID, choose the
// current slot, invalidate replaced slots, verify HTTP bounds/framing and
// freshness upstream, and recheck current selection and permission on delivery.
// Creating another slot for the same request ID is not permitted.
func NewSampleAttempt(ctx Context) *SampleAttempt {
	if ctx.Route != SampleRoute || !ctx.OptIn || !ctx.CallerAuthorized || !ctx.CapabilityAuthorized || !ctx.SessionVerified || !ctx.DiscoveryVerified || !ctx.AttemptActive {
		return &SampleAttempt{}
	}
	if len(ctx.PublicKey) != 32 {
		return &SampleAttempt{}
	}
	ctx.PublicKey = append([]byte(nil), ctx.PublicKey...)
	return &SampleAttempt{ctx: ctx}
}

// Invalidate clears a pending slot at cancellation or replacement.
func (a *SampleAttempt) Invalidate() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ctx = Context{}
}

// Consume processes at most one completion. Any completion, even a bad one,
// uses the slot. Unknown/error bodies and stale slots never expose data.
func (a *SampleAttempt) Consume(status int, signatures []string, body []byte) (Sample, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ctx := a.ctx
	a.ctx = Context{}
	if !ctx.AttemptActive || status != 200 {
		return Sample{}, errors.New("no valid active sample completion")
	}
	raw, err := verifyProof(ctx, signatures, body)
	if err != nil {
		return Sample{}, err
	}
	return decodeSample(raw, ctx.RequestID)
}
