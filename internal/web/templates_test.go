package web

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/store"
)

func TestParseTemplates(t *testing.T) {
	ts, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates: %v", err)
	}

	wantPages := []string{
		"dashboard.html",
		"settings/appearance.html",
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

func TestProductionPagesExecute(t *testing.T) {
	ts, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	stack := &store.Stack{Name: "example", Entrypoint: "compose.pkl"}
	for page, data := range map[string]any{
		"dashboard.html":           &dashboardData{},
		"settings/appearance.html": &pageData{Page: "settings"},
		"stacks/index.html":        &stackListData{Stacks: []*store.Stack{stack}},
		"stacks/new.html":          &stackNewData{},
		"stacks/detail.html":       &stackDetailData{Stack: stack, Files: []string{"compose.pkl"}},
		"stacks/editor.html":       &stackEditorData{Stack: stack, ActiveFile: "compose.pkl"},
		"containers/index.html":    &containerListData{},
		"containers/detail.html":   &containerDetailData{Container: &engine.ContainerInfo{ID: "abc", State: "running"}},
		"images/index.html":        &imageListData{},
		"recipes/index.html":       &recipeListData{ByCategory: map[string][]*store.Recipe{"test": {{Name: "example", Slug: "example"}}}},
		"recipes/editor.html":      &recipeEditorData{Recipe: &store.Recipe{Name: "example", Slug: "example"}},
	} {
		t.Run(page, func(t *testing.T) {
			var out bytes.Buffer
			if err := ts.pages[page].Execute(&out, data); err != nil {
				t.Fatalf("execute %s: %v", page, err)
			}
			body := out.String()
			for _, want := range []string{"<!DOCTYPE html>", "/static/theme.css", "/static/vendor/app.css", `href="/settings"`} {
				if !strings.Contains(body, want) {
					t.Errorf("page missing %q", want)
				}
			}
		})
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
