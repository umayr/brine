package dill

import "context"

// OperationObserver belongs to one durably accepted request, never to shared
// Runner state. Started must succeed before discovery or any operation child.
// Terminal receives only adapter-validated axes, before any HTTP marker write.
// A nil Terminal error attests successful durable publication; any history error
// suppresses the terminal marker rather than presenting an attempted commit.
type OperationObserver interface {
	Started() error
	Terminal(OperationInvocation) error
}

type operationObserverKey struct{}

func WithOperationObserver(ctx context.Context, observer OperationObserver) context.Context {
	return context.WithValue(ctx, operationObserverKey{}, observer)
}

// OperationHistoryError is observer failure, not an engine outcome. Invocation
// preserves any known receipt even when durable history could not be committed.
type OperationHistoryError struct{ Invocation OperationInvocation }

// OperationDeliveryError is observer transport loss, never an engine failure.
type OperationDeliveryError struct{ Invocation OperationInvocation }

func (e *OperationDeliveryError) Error() string {
	return "HTTP delivery unconfirmed; inspect durable request status; do not retry"
}

func (e *OperationHistoryError) Error() string {
	return "Operation history persistence failed; inspect request status and target; do not retry"
}
