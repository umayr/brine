package targetregistry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
)

// HistoryIncarnation identifies one Brine HTTPS enrollment, never an endpoint
// or credential. It must not be sent to the provider or exposed on the wire.
type HistoryIncarnation [16]byte

var ErrHistoryUnenrolled = errors.New("target has no history enrollment")

func parseHistoryIncarnation(v string) (HistoryIncarnation, bool) {
	var id HistoryIncarnation
	if len(v) != hex.EncodedLen(len(id)) {
		return id, false
	}
	if _, err := hex.Decode(id[:], []byte(v)); err != nil || id == (HistoryIncarnation{}) || hex.EncodeToString(id[:]) != v {
		return HistoryIncarnation{}, false
	}
	return id, true
}

func (d disk) validateHistoryIncarnations() error {
	if len(d.HistoryIncarnations) == 0 {
		return nil
	}
	https := map[string]bool{}
	for _, t := range d.Targets {
		if t.HTTPSAddress != "" {
			https[t.ID] = true
		}
	}
	seen := map[HistoryIncarnation]bool{}
	for target, raw := range d.HistoryIncarnations {
		id, ok := parseHistoryIncarnation(raw)
		if !https[target] || !ok || seen[id] {
			return errors.New("invalid history enrollment metadata")
		}
		seen[id] = true
	}
	return nil
}

func (d *disk) assignHistoryIncarnation(target string) error {
	var id HistoryIncarnation
	if _, err := rand.Read(id[:]); err != nil || id == (HistoryIncarnation{}) {
		return errors.New("cannot assign history enrollment")
	}
	v := hex.EncodeToString(id[:])
	for _, existing := range d.HistoryIncarnations {
		if existing == v {
			return errors.New("cannot assign history enrollment")
		}
	}
	if d.HistoryIncarnations == nil {
		d.HistoryIncarnations = make(map[string]string)
	}
	d.HistoryIncarnations[target] = v
	return nil
}

// AcquireHistoryRead returns both values from the same locked registry snapshot.
// Keep the lease until all work using either value ends. This does not authorize
// history access or make a historical incarnation visible after forgetting it.
func (r *Registry) AcquireHistoryRead(ctx context.Context, target string) (Record, HistoryIncarnation, *ReadLease, error) {
	t, id, lease, err := r.acquireTarget(ctx, target)
	if err != nil {
		return Record{}, HistoryIncarnation{}, nil, err
	}
	if id == (HistoryIncarnation{}) {
		return Record{}, HistoryIncarnation{}, nil, errors.Join(ErrHistoryUnenrolled, lease.Close())
	}
	return t, id, lease, nil
}

// InitializeHistoryIncarnation explicitly assigns an ID to a legacy HTTPS
// record with no history metadata. The caller must establish operator approval
// for the exact reviewed record; this API is not a user-facing permission gate.
// No existing ID may be overwritten. No record or observation is migrated.
func (r *Registry) InitializeHistoryIncarnation(ctx context.Context, target, reviewed Record) error {
	if target != reviewed {
		return ErrUnconfirmed
	}
	if target.HTTPSAddress == "" || target.Validate() != nil {
		return ErrMismatch
	}
	return r.lockedContext(ctx, func() error {
		d, err := r.load()
		if err != nil {
			return err
		}
		for _, active := range d.Targets {
			if active.ID != target.ID {
				continue
			}
			if active != target || d.HistoryIncarnations[target.ID] != "" {
				return ErrConflict
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := d.assignHistoryIncarnation(target.ID); err != nil {
				return err
			}
			return r.save(ctx, d)
		}
		return ErrNotFound
	})
}
