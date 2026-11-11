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
		{"output then failure", io.MultiReader(strings.NewReader("first line\n"), failingLogReader{}), "data: <div class=\"log-line\">first line</div>\n\nevent: stream-error", "event: done"},
		{"oversized line", strings.NewReader(strings.Repeat("x", 1024*1024+1)), "event: stream-error", "event: done"},
		{"empty", strings.NewReader(""), "event: done", "event: stream-error"},
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
