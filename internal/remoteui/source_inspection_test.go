package remoteui

import (
	"bytes"
	"github.com/umayr/brine/internal/store"
	"strings"
	"testing"
)

func TestSourceInspectionFormBindsIncidentAndOriginalTarget(t *testing.T) {
	p := deploymentPage{GitSettings: true, Host: "target", Stack: "demo", Source: "source", Target: "current", GitPolicy: &store.RemoteGitPolicy{Enabled: true, Target: "original", Source: "source", Reason: "source-sync-unconfirmed", SourceSync: strings.Repeat("a", 32), Phase: "blocked"}}
	var b bytes.Buffer
	if e := deploymentView.Execute(&b, p); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(b.String(), `value="recover-source"`) || !strings.Contains(b.String(), "requires the original target binding") {
		t.Fatal("replacement target offered impossible inspection")
	}
	p.Target = "original"
	b.Reset()
	if e := deploymentView.Execute(&b, p); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(b.String(), `name="source_sync" value="`+p.GitPolicy.SourceSync+`"`) || !strings.Contains(b.String(), `value="recover-source"`) {
		t.Fatal("inspection did not bind displayed incident")
	}
}
