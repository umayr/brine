package dill

import (
	"bytes"
	"errors"
)

var errCaptureLimit = errors.New("Dill output exceeded capture limit")

// captureBuffer retains a bounded prefix while draining all writes. Each stream
// has its own instance, used by one os/exec copying goroutine until Wait returns.
type captureBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *captureBuffer) String() string { return b.buf.String() }

func (b *captureBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.buf.Len()
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.buf.Write(p)
	return n, nil
}
