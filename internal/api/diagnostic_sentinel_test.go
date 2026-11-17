//go:build unix

package api

// Reference uploads must reject plaintext without echoing/storing it. Strict
// malformed diagnostics are suppressed, whereas a VALID structured plan's
// deployment values are operator-visible and privately persisted by design.
