package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/httpx"
)

type imagePullEngine struct {
	engine.Engine
	reader io.Reader
	err    error
}

func (e imagePullEngine) PullImage(context.Context, string) (io.ReadCloser, error) {
	if e.err != nil {
		return nil, e.err
	}
	return io.NopCloser(e.reader), nil
}

type imagePullFailingReader struct{}

func (imagePullFailingReader) Read([]byte) (int, error) {
	return 0, errors.New("SYNTHETIC_PRIVATE_IMAGE_PULL_READ_ERROR")
}

func TestImagePullStreamTerminalEvent(t *testing.T) {
	const line = "synthetic progress\n"
	const openError = "SYNTHETIC_PRIVATE_IMAGE_PULL_OPEN_ERROR"
	for _, tc := range []struct {
		name              string
		engine            imagePullEngine
		status            int
		contentType, body string
	}{
		{"clean EOF", imagePullEngine{reader: strings.NewReader(line)}, http.StatusOK, "text/event-stream", "data: synthetic progress\n\nevent: done\ndata: pull complete\n\n"},
		{"read failure", imagePullEngine{reader: io.MultiReader(strings.NewReader(line), imagePullFailingReader{})}, http.StatusOK, "text/event-stream", "data: synthetic progress\n\nevent: stream-error\ndata: Image pull progress stream failed; check the local container engine for the pull outcome.\n\n"},
		{"start error", imagePullEngine{err: errors.New(openError)}, http.StatusInternalServerError, "application/json", "{\"error\":\"Image pull unavailable; check the image reference and local container engine\"}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := chi.NewRouter()
			Mount(router, agent.NewLocalAgent(tc.engine, nil), nil, nil, nil, "docker", httpx.Timeouts{}, 0)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/images/pull", strings.NewReader(`{"ref":"synthetic.invalid/example:fixture"}`)))
			if w.Code != tc.status || w.Header().Get("Content-Type") != tc.contentType {
				t.Fatalf("status/type = %d %q, want %d %q", w.Code, w.Header().Get("Content-Type"), tc.status, tc.contentType)
			}
			if strings.Contains(w.Body.String(), openError) {
				t.Fatal("raw image pull open error reached the API response")
			}
			if w.Body.String() != tc.body {
				t.Error("response did not match the expected progress and terminal event or start error")
			}
		})
	}
}
