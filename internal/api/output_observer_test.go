package api

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type deadlineRecorder struct{ *httptest.ResponseRecorder }

func (w *deadlineRecorder) SetWriteDeadline(time.Time) error { return nil }
func TestOutputObserverDeliveryAndJoin(t *testing.T) {
	w := &deadlineRecorder{httptest.NewRecorder()}
	o, err := newOutputObserver(w)
	if err != nil {
		t.Fatal(err)
	}
	o.Write([]byte("progress\n"))
	writeActionStatus(o, "stop", nil)
	o.Finish()
	before := w.Body.String()
	if !strings.Contains(before, "[brine:done]") {
		t.Fatal(before)
	}
	o.Write([]byte("late"))
	o.Finish()
	if before != w.Body.String() {
		t.Fatal("write after Finish")
	}
}
func TestOutputObserverOverflowIsNotOperationFailure(t *testing.T) {
	w := &deadlineRecorder{httptest.NewRecorder()}
	o, err := newOutputObserver(w)
	if err != nil {
		t.Fatal(err)
	}
	p := bytes.Repeat([]byte("x"), 1024*1024)
	if n, err := o.Write(p); n != len(p) || err != nil {
		t.Fatalf("output changed operation: %d %v", n, err)
	}
	writeActionStatus(o, "stop", nil)
	o.Finish()
	if strings.Contains(w.Body.String(), "[brine:done]") || !strings.Contains(w.Body.String(), "Output delivery incomplete") {
		t.Fatal("overflow falsely delivered complete output")
	}
}
func TestOutputObserverRejectsUnboundedWriter(t *testing.T) {
	if _, err := newOutputObserver(httptest.NewRecorder()); err == nil {
		t.Fatal("unsupported deadlines accepted")
	}
}
