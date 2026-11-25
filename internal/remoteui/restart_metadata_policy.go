package remoteui

import (
	"context"
	"time"

	"github.com/umayr/brine/internal/targetregistry"
)

const maxRestartMetadataEnrollments = 64

type restartMetadataApproval struct {
	host        string
	incarnation targetregistry.HistoryIncarnation
}

type restartMetadataEnrollment struct {
	incarnation targetregistry.HistoryIncarnation
	target      string
}

// newRestartMetadataAllowlist verifies explicit startup incarnation pins.
// Host names alone cannot grant permission, even after a process restart.
// Any failed resolution refuses the whole configuration; there is no wildcard,
// partial approval, credential lookup, or fallback to deployment authority.
func newRestartMetadataAllowlist(ctx context.Context, registry *targetregistry.Registry, approvals []restartMetadataApproval) (restartMetadataAccess, error) {
	deny := restartMetadataAccess(func(context.Context, archiveReadActor, targetregistry.Record, targetregistry.HistoryIncarnation) error {
		return errRestartMetadata
	})
	if len(approvals) == 0 {
		return deny, nil
	} // disabled does no registry I/O
	if ctx == nil || ctx.Err() != nil || registry == nil || len(approvals) > maxRestartMetadataEnrollments {
		return nil, errRestartMetadata
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	approved := make(map[string]restartMetadataEnrollment, len(approvals))
	for _, approval := range approvals {
		host := approval.host
		if host == "" || len(host) > 128 || approval.incarnation == (targetregistry.HistoryIncarnation{}) {
			return nil, errRestartMetadata
		}
		if _, exists := approved[host]; exists {
			return nil, errRestartMetadata
		}
		record, id, lease, err := registry.AcquireHistoryRead(ctx, host)
		if err != nil {
			return nil, errRestartMetadata
		}
		closeErr := lease.Close()
		if closeErr != nil || ctx.Err() != nil || record.ID != host || record.HTTPSAddress == "" || id != approval.incarnation {
			return nil, errRestartMetadata
		}
		approved[host] = restartMetadataEnrollment{incarnation: id, target: targetDigest(record)}
	}
	return func(ctx context.Context, actor archiveReadActor, record targetregistry.Record, id targetregistry.HistoryIncarnation) error {
		if ctx == nil || ctx.Err() != nil || actor.operator == "" || !attendedHex(actor.session, 32) {
			return errRestartMetadata
		}
		pin, ok := approved[record.ID]
		if !ok || pin.incarnation != id || pin.target != targetDigest(record) {
			return errRestartMetadata
		}
		return nil
	}, nil
}
