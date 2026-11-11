package web

import (
	"bytes"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/engine"
)

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
