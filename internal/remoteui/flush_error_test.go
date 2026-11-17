package remoteui

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

type errorFlushWriter struct {
	*httptest.ResponseRecorder
	calls int
}

func (w *errorFlushWriter) FlushError() error { w.calls++; return errors.New("synthetic") }

type countStream struct{ calls int }

func (s *countStream) Next(context.Context) (LogEvent, error) {
	s.calls++
	return LogEvent{Type: "chunk", Stream: "stdout", Bytes: []byte("x")}, nil
}
func (*countStream) Close() error { return nil }
func TestLogStreamStopsOnFlushError(t *testing.T) {
	underlying := &errorFlushWriter{ResponseRecorder: httptest.NewRecorder()}
	w := &sessionWriter{ResponseWriter: underlying, ctx: context.Background()}
	stream := &countStream{}
	writeLogStream(context.Background(), w, stream)
	if underlying.calls != 1 || stream.calls != 1 {
		t.Fatal("flush failure did not stop stream", underlying.calls, stream.calls)
	}
}
