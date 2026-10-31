package web

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/engine"
)

func TestParseTemplates(t *testing.T) {
	ts, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}

	wantPages := []string{
		"dashboard.html",
		"stacks/index.html",
		"stacks/new.html",
		"stacks/detail.html",
		"stacks/editor.html",
		"containers/index.html",
		"containers/detail.html",
		"images/index.html",
		"recipes/index.html",
		"recipes/editor.html",
	}
	for _, page := range wantPages {
		if _, ok := ts.pages[page]; !ok {
			t.Errorf("page template %q not found", page)
		}
	}

	wantPartials := []string{
		"container-rows",
		"plan-diff",
		"stack-containers",
		"image-rows",
	}
	for _, partial := range wantPartials {
		if _, ok := ts.partials[partial]; !ok {
			t.Errorf("partial template %q not found", partial)
		}
	}
}

func TestImageRowsUntagged(t *testing.T) {
	ts, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	data := []engine.ImageInfo{{
		ID:      "abcdef123456",
		Tags:    nil,
		Size:    1024,
		Created: time.Now(),
	}}
	var buf bytes.Buffer
	if err := ts.partials["image-rows"].Execute(&buf, data); err != nil {
		t.Fatalf("image-rows with untagged image: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "untagged") {
		t.Errorf("expected untagged placeholder, got %q", out)
	}
	if !strings.Contains(out, "Remove abcdef123456?") {
		t.Errorf("expected ID fallback in confirm text, got %q", out)
	}
}

func TestPlanDiffHeaderCountsOnlyChanges(t *testing.T) {
	ts, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}
	result := &dill.PlanResult{
		Changes: []dill.PlanChange{
			{Service: "web", Kind: "noop"},
			{Service: "api", Kind: "noop"},
		},
		Summary: dill.PlanSummary{Noop: 2},
	}
	var buf bytes.Buffer
	if err := ts.partials["plan-diff"].Execute(&buf, result); err != nil {
		t.Fatalf("plan-diff: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "0 changes") {
		t.Errorf("expected 0 changes in header, got %q", out)
	}
	if strings.Contains(out, "2 changes") {
		t.Errorf("header still counts noops: %q", out)
	}
}
