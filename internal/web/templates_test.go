package web

import (
	"testing"
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
