package server

import (
	"context"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/webhooks"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalWebhookNeverReclaimsInspectedRemoteSource(t *testing.T) {
	s, fs, _, calls := dispatcherServer(t)
	d := s.webhookDispatcher.(*webhookDispatcher)
	stack, _ := fs.Get("example")
	intent := store.ManualDeploymentIntent{Host: "remote", Target: strings.Repeat("a", 64), Stack: stack.Name, Mode: "create", Request: strings.Repeat("b", 32), Journal: strings.Repeat("c", 32), Selection: "sha256:" + strings.Repeat("d", 64), Stage: strings.Repeat("e", 32), Manifest: strings.Repeat("f", 64)}
	if e := fs.SaveManualIntent(intent); e != nil {
		t.Fatal(e)
	}
	if e := fs.ResolveManualIntent(intent); e != nil {
		t.Fatal(e)
	}
	for _, action := range webhooks.Actions {
		grant := webhooks.Trigger{StackID: webhookStackID(stack), TargetID: d.localTargetID(), Action: action}
		if d.Validate(context.Background(), grant) == nil {
			t.Fatal("resolved remote evidence became local execution authority", action)
		}
		result := d.Execute(context.Background(), grant, webhooks.IncomingRequest{ID: webhooks.NewOperationID()})
		if result.Status != "rejected" || result.Reason != "admission_denied" {
			t.Fatal("inspected source fell back to local", action, result)
		}
	}
	opts, e := d.WebhookOptions(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	for _, option := range opts {
		if option.StackID == webhookStackID(stack) && option.TargetID == d.localTargetID() {
			t.Fatal("inspected remote source listed as local")
		}
	}
	if _, e := os.Stat(calls); !os.IsNotExist(e) {
		t.Fatal("remote-owned source executed local child")
	}
}
func TestDisabledRemoteGitNeverBecomesLocalWebhookGrant(t *testing.T) {
	s, fs, _, calls := dispatcherServer(t)
	d := s.webhookDispatcher.(*webhookDispatcher)
	fs.Delete("example")
	repo, e := fs.RepoDir("example")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(repo, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(repo, "compose.pkl"), []byte("config { engine = \"docker\" }\n"), 0600); e != nil {
		t.Fatal(e)
	}
	stack, e := fs.CreateFromGit("example", &store.GitSource{URL: "https://example.invalid/fixture.git", Branch: "main"}, "compose.pkl")
	if e != nil {
		t.Fatal(e)
	}
	source := stack.RemoteGitSource()
	policy := &store.RemoteGitPolicy{Host: "remote", Target: strings.Repeat("a", 64), Source: source, Phase: "disabled"}
	if e = fs.SwapRemoteGit("example", source, nil, policy); e != nil {
		t.Fatal(e)
	}
	for _, action := range webhooks.Actions {
		grant := webhooks.Trigger{StackID: webhookStackID(stack), TargetID: d.localTargetID(), Action: action}
		if d.Validate(context.Background(), grant) == nil {
			t.Fatal("disabled remote binding offered local action", action)
		}
		result := d.Execute(context.Background(), grant, webhooks.IncomingRequest{ID: webhooks.NewOperationID()})
		if result.Status != "rejected" || result.Reason != "admission_denied" {
			t.Fatal("disabled binding bypassed execution admission", action, result)
		}
	}
	opts, e := d.WebhookOptions(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	for _, option := range opts {
		if option.TargetID == d.localTargetID() {
			t.Fatal("disabled remote source listed as local")
		}
	}
	if _, e = os.Stat(calls); !os.IsNotExist(e) {
		t.Fatal("disabled remote source invoked local child")
	}
}
func TestLocalEngineAdmissionMatrixIsNotRuntimeQualification(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		t.Run(engine, func(t *testing.T) {
			s, fs, r, _ := dispatcherServer(t)
			r.Engine = engine
			s.cfg.Engine = engine
			if _, e := fs.Update("example", "config { engine = \""+engine+"\" }\n"); e != nil {
				t.Fatal(e)
			}
			st, _ := fs.Get("example")
			d := s.webhookDispatcher.(*webhookDispatcher)
			for _, action := range webhooks.Actions {
				grant := webhooks.Trigger{StackID: webhookStackID(st), TargetID: d.localTargetID(), Action: action}
				e := d.Validate(context.Background(), grant)
				if action == "git-sync" {
					if e == nil {
						t.Fatal("workspace offered Git sync")
					}
				} else if e != nil {
					t.Fatal("ordinary local engine admission denied", engine, action, e)
				}
			}
			r.Profile = "alpha"
			for _, action := range webhooks.Actions {
				if d.Validate(context.Background(), webhooks.Trigger{StackID: webhookStackID(st), TargetID: d.localTargetID(), Action: action}) == nil {
					t.Fatal("retired mode became local fallback", engine, action)
				}
			}
		})
	}
}
