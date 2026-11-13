package web

import (
	"strings"
	"testing"
)

func TestQualifiedDeleteGuidanceDoesNotPromiseAnAPIDeleteUnlock(t *testing.T) {
	data, err := templateFS.ReadFile("templates/stacks/detail.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `{{if .QualifiedOperations}}disabled title="Qualified stack deletion is deferred; preserve source and operation recovery context. Use explicit Down and inspect its receipt."{{end}}`) {
		t.Fatal("qualified Delete must explain unconditional deferral and preserve recovery context")
	}
	if strings.Contains(text, "before deleting metadata via the API") {
		t.Fatal("Down must not promise to unlock the unconditionally refused qualified DELETE route")
	}
}
