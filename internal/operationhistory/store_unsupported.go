//go:build !linux

package operationhistory

// Durable Alpha operation history requires native Linux descriptor/ACL checks.
func openBackend(string) (backend, error) { return nil, ErrUnsafe }
