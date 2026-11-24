package api

import "bytes"

// diagnosticOutput drains output without I/O or unbounded allocation. Used only
// for delete failure diagnostics, never as a complete operation receipt.
type diagnosticOutput struct {
	buf       bytes.Buffer
	truncated bool
}

func (b *diagnosticOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (64 << 10) - b.buf.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.buf.Write(p)
	return n, nil
}
func (b *diagnosticOutput) String() string {
	s := b.buf.String()
	if b.truncated {
		s += "\n[diagnostic output truncated]"
	}
	return s
}
