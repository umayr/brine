//go:build unix

package dill

// Strict diagnostics are discarded, not heuristically scrubbed. Valid structured
// plans may deliberately contain deployment values; cleanup streams remain
// authenticated operator diagnostics even with a strict controller installed.
