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

type failingLogReader struct{}

func (failingLogReader) Read([]byte) (int, error) {
	return 0, errors.New("SYNTHETIC_PRIVATE_LOG_READ_ERROR")
}

type logReadEngine struct {
	engine.Engine
	reader io.Reader
}

func (e logReadEngine) ContainerLogs(context.Context, string, bool, int) (io.ReadCloser, error) {
	return io.NopCloser(e.reader), nil
}

func TestContainerLogTerminalDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reader   io.Reader
		terminal string
	}{
		{"clean EOF", strings.NewReader("synthetic line\n"), "stream ended"},
		{"read failure", io.MultiReader(strings.NewReader("synthetic line\n"), failingLogReader{}), "Log stream incomplete; check the local container engine and retry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := chi.NewRouter()
			Mount(router, agent.NewLocalAgent(logReadEngine{reader: tc.reader}, nil), nil, nil, nil, "docker", httpx.Timeouts{}, 0)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/containers/demo/logs", nil))
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/event-stream" {
				t.Fatalf("unexpected log stream status/type: %d %q", w.Code, w.Header().Get("Content-Type"))
			}
			want := "data: synthetic line\n\nevent: done\ndata: " + tc.terminal + "\n\n"
			if w.Body.String() != want {
				t.Error("log stream did not return the expected line and safe terminal diagnostic")
			}
		})
	}
}
