package dill

import (
	"bytes"
	"errors"
	"testing"
)

type testObserver struct {
	start  func() error
	finish func(OperationInvocation) error
}

func (o testObserver) Started() error                       { return o.start() }
func (o testObserver) Terminal(v OperationInvocation) error { return o.finish(v) }

type observerWriter struct {
	t        *testing.T
	terminal *bool // successful persistence, not merely callback entry
	calls    *int
	output   *bytes.Buffer
	fail     bool
}

func (w observerWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	*w.calls++
	if !*w.terminal {
		w.t.Error("HTTP write occurred without successful terminal persistence")
	}
	if w.fail {
		return 0, errors.New("delivery lost")
	}
	if w.output != nil {
		return w.output.Write(p)
	}
	return len(p), nil
}
