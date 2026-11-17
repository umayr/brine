package web

import (
	"bytes"
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRemoteNavigationRequiresEnablement(t *testing.T) {
	tmpl, err := template.New("layout.html").ParseFS(templateFS, "templates/layout.html")
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{}
	r := httptest.NewRequest("GET", "/", nil)
	render := func() string {
		t.Helper()
		var b bytes.Buffer
		if err := tmpl.Execute(&b, h.withGlobalTemplateData(r, &pageData{})); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	if strings.Contains(render(), `href="/remote"`) {
		t.Fatal("disabled navigation visible")
	}
	h.EnableRemoteNavigation()
	if !strings.Contains(render(), `href="/remote"`) {
		t.Fatal("enabled navigation absent")
	}
}
