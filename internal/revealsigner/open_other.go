//go:build !linux && !darwin

package revealsigner

// Open fails closed on platforms without the supported file checks.
func Open(path string) (*Signer, error) { return nil, ErrKey }
