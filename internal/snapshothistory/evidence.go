package snapshothistory

import (
	"context"
	"time"
)

// DeliveryState describes local evidence, not the current target or whether an
// execution can safely be repeated.
type DeliveryState string

const (
	LocalIntentOnly DeliveryState = "local-intent-only"
	DeliveryUnknown DeliveryState = "delivery-unknown"
	VerifiedReceipt DeliveryState = "verified-receipt"
)

// ReceiptBinding is independently obtained from the verified original
// connection and response. The provider body alone cannot prove Target,
// Scope, AuthorityRevision or Journal. Operation IDs are never lookup keys.
type ReceiptBinding struct {
	Target, Scope, AuthorityRevision, Journal, RequestID, Stack string
}

// TerminalObservation is a sanitized value supplied by the trusted in-process
// consumer after it validated the original target and distinct grants, the
// exact single journal echo, and the provider document. The archive does not
// perform transport verification or supply authority. This seam is unmounted.
// Provenance is "journal-apply" or "journal-status"; version identifies the
// qualified provider contract, not a version inferred from the response.
type TerminalObservation struct {
	Binding                     ReceiptBinding
	State, Effects, OperationID string
	Provenance, ContractVersion string
	ObservedAt                  time.Time
}

// Evidence separates the optional terminal receipt from the immutable original
// intent and any local marker. UnknownSince may precede any actual send.
type Evidence struct {
	State        DeliveryState
	UnknownSince time.Time
	Receipt      *TerminalObservation
}

// MarkDeliveryUnknown records a conservative boundary before a one-shot send
// could happen. It does not prove a send or authorize one. Callers must not
// retry an indeterminate result; the marker can outlive a crash before send.
func (s *Store) MarkDeliveryUnknown(ctx context.Context, request OpenRequest, at time.Time) error {
	if s == nil || ctx == nil || at.IsZero() {
		return ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.impl == nil {
		return ErrUnavailable
	}
	return s.impl.appendEvidence(ctx, request, evidenceMarker, at.UTC(), nil)
}

// RecordTerminalObservation appends one immutable, corroborated receipt after
// a durable unknown marker. Running, unknown, fence results, transport errors,
// and absence are not terminal receipts. A later write never replaces a receipt.
func (s *Store) RecordTerminalObservation(ctx context.Context, request OpenRequest, result TerminalObservation) error {
	if s == nil || ctx == nil || result.ObservedAt.IsZero() {
		return ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.impl == nil {
		return ErrUnavailable
	}
	result.ObservedAt = result.ObservedAt.UTC()
	return s.impl.appendEvidence(ctx, request, evidenceReceipt, result.ObservedAt, &result)
}

// ReadEvidence requires the full independently retained OpenRequest. It never
// sends, infers admission, retries, or projects history to another target.
func (s *Store) ReadEvidence(ctx context.Context, request OpenRequest) (Evidence, error) {
	if s == nil || ctx == nil {
		return Evidence{}, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.impl == nil {
		return Evidence{}, ErrUnavailable
	}
	return s.impl.readEvidence(ctx, request)
}

const (
	evidenceMarker  = "delivery-unknown"
	evidenceReceipt = "terminal-receipt"
)
