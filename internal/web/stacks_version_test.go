package web

import (
	"bytes"
	"strings"
	"testing"
)

func TestDefaultStackPklUsesDill050(t *testing.T) {
	for _, engine := range []string{"", "docker", "podman"} {
		name := engine
		if name == "" {
			name = "default"
		}
		t.Run(name, func(t *testing.T) {
			source := defaultStackPkl(engine)
			if !strings.HasPrefix(source, `amends "package://pkg.ushd.ca/dill@0.5.0#/dill.pkl"`) {
				t.Fatalf("starter must use the bundled Dill schema: %s", source)
			}
			wantEngine := engine
			if wantEngine == "" {
				wantEngine = "docker"
			}
			if !strings.Contains(source, `engine = "`+wantEngine+`"`) {
				t.Fatalf("starter does not select %q: %s", wantEngine, source)
			}
			if !strings.Contains(source, `name = "my-app"`) {
				t.Fatal("starter must retain the stack-name placeholder")
			}
		})
	}
}

func TestNewStackPageUsesDill050(t *testing.T) {
	ts, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	data := &stackNewData{
		pageData:   pageData{Page: "stacks"},
		DefaultPkl: defaultStackPkl("docker"),
	}
	if err := ts.pages["stacks/new.html"].Execute(&body, data); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(body.String(), "package://pkg.ushd.ca/dill@0.5.0#/dill.pkl"); got != 2 {
		t.Fatalf("want current schema in starter and placeholder, got %d references", got)
	}
	if strings.Contains(body.String(), "dill@0.4.0") {
		t.Fatal("new stack page must not offer the old schema")
	}
}
