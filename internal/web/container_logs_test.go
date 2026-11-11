package web

import (
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

type failingLogReader struct{}

func (failingLogReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestStreamContainerLogs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reader   io.Reader
		want     string
		unwanted string
	}{
		{"completed", strings.NewReader("<hello> & world\n"), "data: <div class=\"log-line\">&lt;hello&gt; &amp; world</div>\n\nevent: done", "event: stream-error"},
		{"failed", failingLogReader{}, "event: stream-error", "event: done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			streamContainerLogs(w, tc.reader, w, true)
			if body := w.Body.String(); !strings.Contains(body, tc.want) || strings.Contains(body, tc.unwanted) {
				t.Fatalf("unexpected stream: %q", body)
			}
		})
	}
}
