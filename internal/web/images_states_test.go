package web

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/engine"
)

type imageListEngine struct {
	engine.Engine
	images []engine.ImageInfo
	err    error
}

func (e imageListEngine) ListImages(context.Context) ([]engine.ImageInfo, error) {
	return e.images, e.err
}

func TestImageRowsHandlerStates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		engine imageListEngine
		want   string
	}{
		{"empty", imageListEngine{}, "No images found."},
		{"engine error", imageListEngine{err: errors.New("engine <offline>")}, "engine &lt;offline&gt;"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{agent: agent.NewLocalAgent(tc.engine, nil)}
			w := httptest.NewRecorder()
			h.imageRows(w, httptest.NewRequest("GET", "/web/images", nil))
			if w.Code != 200 || !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("status %d, body %s; want %q", w.Code, w.Body.String(), tc.want)
			}
		})
	}
}

func TestImageInventoryStates(t *testing.T) {
	ts, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data any
		want []string
	}{
		{"image-rows", []engine.ImageInfo{}, []string{"No images found.", "configured engine returned no images"}},
		{"image-list-error", "engine <offline>", []string{"Inventory is unavailable", "engine &lt;offline&gt;", "Retry", "colspan=\"5\""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := ts.partials[tc.name].Execute(&out, tc.data); err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q in %s", want, out.String())
				}
			}
		})
	}
}
