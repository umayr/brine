package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type nonflushingOutput struct{}

func (nonflushingOutput) Header() http.Header              { return make(http.Header) }
func (nonflushingOutput) WriteHeader(int)                  {}
func (nonflushingOutput) Write(p []byte) (int, error)      { return len(p), nil }
func (nonflushingOutput) SetWriteDeadline(time.Time) error { return nil }

type shortOutput struct{ *deadlineRecorder }

func (w shortOutput) Write(p []byte) (int, error) { return w.deadlineRecorder.Write(p[:len(p)-1]) }
func TestObserverRejectsNonflushingWriter(t *testing.T) {
	if _, err := newOutputObserver(nonflushingOutput{}); err == nil {
		t.Fatal("missing flush capability accepted")
	}
}
func TestObserverShortWriteNeverReportsDone(t *testing.T) {
	w := shortOutput{&deadlineRecorder{httptest.NewRecorder()}}
	o, err := newOutputObserver(w)
	if err != nil {
		t.Fatal(err)
	}
	o.Write([]byte("progress\n"))
	writeActionStatus(o, "stop", nil)
	o.Finish()
	if strings.Contains(w.Body.String(), "[brine:done]") {
		t.Fatal("short output accepted")
	}
	o.mu.Lock()
	failed := o.failed
	o.mu.Unlock()
	if !failed {
		t.Fatal("short write not detected")
	}
}
