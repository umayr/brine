//go:build linux

package snapshothistory

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

const (
	markerPrefix          = "unknown-"
	receiptPrefix         = "receipt-"
	evidencePendingPrefix = ".evidence-pending-"
	maxEvidenceBytes      = 4096
	evidenceVersion       = "brine.attended-evidence/1"
)

type evidencePayload struct {
	Version string               `json:"version"`
	At      time.Time            `json:"at"`
	Receipt *TerminalObservation `json:"receipt,omitempty"`
}

func evidenceName(id ObjectID, kind string) string {
	prefix := markerPrefix
	if kind == evidenceReceipt {
		prefix = receiptPrefix
	}
	return prefix + hex.EncodeToString(id[:])
}

func validEvidencePending(name string) bool {
	suffix, ok := strings.CutPrefix(name, evidencePendingPrefix)
	return ok && canonicalHex(suffix, 16)
}

func parseEvidenceName(name string) (ObjectID, string, bool) {
	for _, kind := range []string{evidenceMarker, evidenceReceipt} {
		prefix := markerPrefix
		if kind == evidenceReceipt {
			prefix = receiptPrefix
		}
		if suffix, ok := strings.CutPrefix(name, prefix); ok && canonicalHex(suffix, 32) {
			var id ObjectID
			_, _ = hex.Decode(id[:], []byte(suffix))
			if id != (ObjectID{}) {
				return id, kind, true
			}
		}
	}
	return ObjectID{}, "", false
}

func validReceipt(r *TerminalObservation, i Intent) bool {
	a := i.Attended
	if r == nil || a == nil || r.ObservedAt.IsZero() || len(r.ContractVersion) == 0 || len(r.ContractVersion) > 64 ||
		(r.Provenance != "journal-apply" && r.Provenance != "journal-status") {
		return false
	}
	for _, c := range r.ContractVersion {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '/') {
			return false
		}
	}
	if !canonicalHex(r.Binding.Journal, 16) || !canonicalHex(r.Binding.RequestID, 16) || r.Binding != (ReceiptBinding{
		Target: a.Target, Scope: string(i.ApprovedTargetScope), AuthorityRevision: a.AuthorityRevision,
		Journal: string(i.OriginalJournal), RequestID: string(i.ProviderRequestID), Stack: a.Stack,
	}) {
		return false
	}
	// Do not persist non-terminal statuses or fence tombstones. Treat a failed
	// result with a missing op ID conservatively rather than guessing identity.
	if !strings.HasPrefix(r.OperationID, "op_") || !canonicalHex(strings.TrimPrefix(r.OperationID, "op_"), 16) {
		return false
	}
	return r.State == "succeeded" && r.Effects == "complete" || r.State == "failed" && (r.Effects == "none" || r.Effects == "partial")
}

func (s *store) loadEvidence(id ObjectID, kind string) (evidencePayload, error) {
	var p evidencePayload
	sealed, err := s.readFile(evidenceName(id, kind), maxEvidenceBytes)
	if err != nil {
		return p, err
	}
	plain, err := openSealed(s.key, s.storeID, id, kind, sealed)
	if err != nil || json.Unmarshal(plain, &p) != nil || p.Version != evidenceVersion || p.At.IsZero() ||
		(kind == evidenceMarker && p.Receipt != nil) || (kind == evidenceReceipt && (p.Receipt == nil || !p.At.Equal(p.Receipt.ObservedAt))) {
		return evidencePayload{}, ErrUnavailable
	}
	canonical, err := json.Marshal(p)
	if err != nil || !bytes.Equal(plain, canonical) {
		return evidencePayload{}, ErrUnavailable
	}
	return p, nil
}

// Only an authenticated terminal receipt with complete or no effects clears
// this object's local blocker. Partial or missing evidence never does.
func (s *store) attendedTerminal(id ObjectID, intent Intent) (bool, error) {
	p, err := s.loadEvidence(id, evidenceReceipt)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !validReceipt(p.Receipt, intent) {
		return false, ErrUnavailable
	}
	marker, err := s.loadEvidence(id, evidenceMarker)
	if err != nil || p.At.Before(marker.At) {
		return false, ErrUnavailable
	}
	return p.Receipt.Effects != "partial", nil
}

func (s *store) checkEvidence(ctx context.Context, id ObjectID, kind string) error {
	parent, err := s.loadContext(ctx, id)
	if err != nil || parent.Intent.Attended == nil {
		return ErrUnavailable
	}
	p, err := s.loadEvidence(id, kind)
	if err != nil {
		return ErrUnavailable
	}
	if kind == evidenceReceipt {
		marker, err := s.loadEvidence(id, evidenceMarker)
		if err != nil || p.At.Before(marker.At) || !validReceipt(p.Receipt, parent.Intent) {
			return ErrUnavailable
		}
	}
	return nil
}

func (s *store) readEvidence(ctx context.Context, request OpenRequest) (Evidence, error) {
	parent, err := s.open(ctx, request)
	if err != nil || parent.Intent.Attended == nil {
		return Evidence{}, ErrUnavailable
	}
	marker, err := s.loadEvidence(request.ObjectID, evidenceMarker)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Evidence{}, ErrUnavailable
	}
	marked := err == nil
	p, err := s.loadEvidence(request.ObjectID, evidenceReceipt)
	if ctx.Err() != nil {
		return Evidence{}, ErrUnavailable
	}
	if errors.Is(err, os.ErrNotExist) {
		if !marked {
			return Evidence{State: LocalIntentOnly}, nil
		}
		return Evidence{State: DeliveryUnknown, UnknownSince: marker.At}, nil
	}
	if err != nil || !marked || p.At.Before(marker.At) || !validReceipt(p.Receipt, parent.Intent) {
		return Evidence{}, ErrUnavailable
	}
	return Evidence{State: VerifiedReceipt, UnknownSince: marker.At, Receipt: p.Receipt}, nil
}

func (s *store) appendEvidence(ctx context.Context, request OpenRequest, kind string, at time.Time, receipt *TerminalObservation) error {
	current, err := s.readEvidence(ctx, request)
	if err != nil {
		return err
	}
	if kind == evidenceMarker && current.State != LocalIntentOnly || kind == evidenceReceipt && current.State == VerifiedReceipt {
		return ErrExists
	}
	if kind == evidenceReceipt && (current.State != DeliveryUnknown || at.Before(current.UnknownSince) || !validReceipt(receipt, request.Intent)) {
		return ErrUnavailable
	}
	key := operationKey(request.Intent.ApprovedTargetScope, request.Intent.Attended.Stack)
	if s.attended == nil || !s.attended.contains(request.ObjectID, key) {
		s.failed = true
		return ErrUnavailable
	}
	plain, err := json.Marshal(evidencePayload{Version: evidenceVersion, At: at, Receipt: receipt})
	if err != nil {
		return ErrUnavailable
	}
	data, err := seal(s.key, s.storeID, request.ObjectID, kind, plain)
	if err != nil || len(data) > maxEvidenceBytes {
		return ErrUnavailable
	}
	u, err := s.scan(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	if u.entries >= maxArchiveEntries || u.bytes > s.maxBytes-int64(len(data)) {
		return ErrCapacity
	}
	// Evidence and its remnants consume bytes and entries, but attach to an
	// existing operation and cannot reserve a new operation slot.
	if ctx.Err() != nil {
		return ErrUnavailable
	}
	if err := writeWithPendingPrefix(s.root, evidenceName(request.ObjectID, kind), data, evidencePendingPrefix); err != nil {
		if errors.Is(err, ErrIndeterminate) {
			s.failed = true
		}
		return err
	}
	if kind == evidenceReceipt && receipt.Effects != "partial" {
		s.attended.remove(request.ObjectID, key)
	}
	return nil
}
