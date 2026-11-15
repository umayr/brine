package api

import (
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/operationhistory"
)

// Narrow internal adapter; only the concrete durable store is used by mounts.
// Test doubles do not attest persistence or enable unsupported platforms.
type attendedHistory interface {
	BeginPreview(operationhistory.BoundIntent) (attendedRequest, error)
	BeginApply(operationhistory.BoundIntent, string, string) (attendedRequest, error)
	GetPreview(string) (operationhistory.RecordV2, error)
}
type attendedRequest interface {
	ID() string
	Started() error
	TerminalPreview(dill.PreflightInvocation) error
	TerminalApply(dill.OperationInvocation) error
	Delivery(bool) error
}
type durableAttendedHistory struct{ store *operationhistory.Store }

func (h durableAttendedHistory) BeginPreview(i operationhistory.BoundIntent) (attendedRequest, error) {
	return h.store.BeginPreviewV2(i)
}
func (h durableAttendedHistory) BeginApply(i operationhistory.BoundIntent, p, d string) (attendedRequest, error) {
	return h.store.BeginApplyV2(i, p, d)
}
func (h durableAttendedHistory) GetPreview(id string) (operationhistory.RecordV2, error) {
	return h.store.GetV2(id)
}
