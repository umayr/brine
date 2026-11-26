package web

import (
	"bytes"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/planstate"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/target"
)

func TestProjectInspectionReachableWithoutPlanning(t *testing.T) {
	templates, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, blocked := range []bool{true, false} {
		data := &stackDetailData{Stack: &store.Stack{Name: "demo"}, EngineAlignment: engineAlignment{Blocked: blocked}, StagingEnabled: true}
		var out bytes.Buffer
		if e := templates.pages["stacks/detail.html"].Execute(&out, data); e != nil {
			t.Fatal(e)
		}
		html := out.String()
		pos := strings.Index(html, "<section id=\"project-preview\"")
		endCompose := strings.Index(html, "<section id=\"panel-Containers\"")
		if pos < 0 || strings.Count(html, "<section id=\"project-preview\"") != 1 || pos >= endCompose {
			t.Fatalf("incorrect inspection panel position when blocked=%v", blocked)
		}
		if !strings.Contains(html[pos:], "Prepare local transfer review") {
			t.Fatal("missing separate transfer control")
		}
	}
}
func TestEndpointLabelsAndVariableFreePlanTags(t *testing.T) {
	for _, tc := range []struct{ socket, label string }{{"/run/docker.sock", "Local"}, {"tcp://example.invalid:2376", "Network endpoint"}, {"", "Unconfirmed"}} {
		if got := target.Configured("", "docker", tc.socket).Location(); got != tc.label {
			t.Fatalf("selector %q: %s", tc.socket, got)
		}
	}
	s := &store.Stack{Name: "demo", Entrypoint: "a.pkl"}
	for _, profile := range []string{"legacy", "alpha"} {
		run := &dill.Runner{Profile: profile}
		tag, e := planstate.Tag(s, run)
		if e != nil || len(tag) != 64 {
			t.Fatal("missing tag without variable store")
		}
		other := *s
		other.Entrypoint = "b.pkl"
		next, e := planstate.Tag(&other, run)
		if e != nil || next == tag {
			t.Fatal("registration change did not invalidate tag")
		}
	}
}
