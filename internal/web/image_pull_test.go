package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/engine"
)

type webImagePullEngine struct {
	engine.Engine
	reader io.Reader
	err    error
	ref    string
}

func (e *webImagePullEngine) PullImage(_ context.Context, ref string) (io.ReadCloser, error) {
	e.ref = ref
	if e.err != nil {
		return nil, e.err
	}
	return io.NopCloser(e.reader), nil
}

type failedPullReader struct{}

func (failedPullReader) Read([]byte) (int, error) { return 0, errors.New("private reader detail") }

func TestWebImagePullOutcomes(t *testing.T) {
	const progress = "{\"status\":\"private progress detail\"}\n"
	for _, tc := range []struct {
		name   string
		stream string
		err    error
		broken bool
		want   string
	}{
		{name: "clean progress", stream: progress, want: "Refresh image inventory to verify the image"},
		{name: "reported failure after progress", stream: progress + `{"error":"private engine detail"}`, want: "engine reported an image pull error"},
		{name: "error detail", stream: `{"errorDetail":{"message":"private engine detail"}}`, want: "engine reported an image pull error"},
		{name: "later error after unknown progress", stream: "invalid\n" + `{"error":"private engine detail"}`, want: "engine reported an image pull error"},
		{name: "startup error", err: errors.New("private engine detail"), want: "Could not start reading image pull progress"},
		{name: "reader error after valid progress", stream: progress, broken: true, want: imagePullUnconfirmed},
		{name: "malformed tail after valid progress", stream: progress + `{"status":`, want: imagePullUnconfirmed},
		{name: "unrecognized record", stream: progress + `{"id":"private unknown detail"}`, want: imagePullUnconfirmed},
		{name: "null is not progress", stream: "null\n", want: imagePullUnconfirmed},
		{name: "empty response", stream: "\n ", want: imagePullUnconfirmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reader io.Reader = strings.NewReader(tc.stream)
			if tc.broken {
				reader = io.MultiReader(reader, failedPullReader{})
			}
			eng := &webImagePullEngine{reader: reader, err: tc.err}
			h := &Handler{agent: agent.NewLocalAgent(eng, nil)}
			r := httptest.NewRequest(http.MethodPost, "/web/images/pull", strings.NewReader(url.Values{"ref": {" example:local "}}.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			h.imagePull(w, r)
			body := w.Body.String()
			if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") || eng.ref != "example:local" ||
				!strings.Contains(body, tc.want) || strings.Contains(body, "private") || strings.Contains(body, "event: done") || strings.Contains(body, "pull complete") {
				t.Fatalf("status %d headers %v ref %q body %q", w.Code, w.Header(), eng.ref, body)
			}
		})
	}
}

func TestWebImagePullValidation(t *testing.T) {
	for _, tc := range []struct{ form, want string }{
		{"ref=%ZZ", "Invalid image pull form"},
		{"ref=++", "Image reference is required"},
	} {
		eng := &webImagePullEngine{}
		h := &Handler{agent: agent.NewLocalAgent(eng, nil)}
		r := httptest.NewRequest(http.MethodPost, "/web/images/pull", strings.NewReader(tc.form))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.imagePull(w, r)
		if w.Code != http.StatusOK || eng.ref != "" || !strings.Contains(w.Body.String(), tc.want) {
			t.Fatalf("form %q: status %d ref %q body %q", tc.form, w.Code, eng.ref, w.Body.String())
		}
	}
}
