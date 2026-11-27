package remoteui

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	gitpkg "github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/targetregistry"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

type failedAutomationResolution struct{ *store.FSStore }

func (f failedAutomationResolution) ResolveManualIntent(store.ManualDeploymentIntent) error {
	return store.ErrManualIntent
}

type failedSourceRollback struct{ *store.FSStore }

func (f failedSourceRollback) SwapRemoteGit(name, source string, expected, next *store.RemoteGitPolicy) error {
	if expected != nil && next != nil && expected.SourceSync != "" && next.SourceSync == "" {
		return store.ErrRemoteGit
	}
	return f.FSStore.SwapRemoteGit(name, source, expected, next)
}

func TestAutomationLifecycleBindsReviewedHeadAndPreservesRecovery(t *testing.T) {
	ctx := context.Background()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	monitor := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("m", 32)))
	deploy := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	const socket = "unix:///run/docker.sock"
	const engine = "11111111-1111-4111-8111-111111111111"
	journal, head := strings.Repeat("a", 32), strings.Repeat("b", 32)
	calls := 0
	ownerPreviews := 0
	replaceOnPreview := 0
	var sourceOnlyCaps atomic.Bool
	unknown := false
	var capsUnavailable atomic.Bool
	raw, e := os.ReadFile("../dill/testdata/preflight-v1/preview-up-config-noop.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct{ Stdout map[string]any }
	json.Unmarshal(raw, &fixture)
	plan := fixture.Stdout
	plan["operation"] = "stop"
	plan["stack"] = "demo"
	plan["target"].(map[string]any)["endpointDigest"] = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(socket)))
	for _, v := range plan["resources"].([]any) {
		r := v.(map[string]any)
		if r["kind"] == "container" {
			r["action"] = "stop"
		} else {
			r["action"] = "retain"
		}
	}
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := deploy
		if r.URL.Path == "/v1/session" || r.URL.Path == "/v1/capabilities" {
			expected = monitor
		}
		if r.Header.Get("Authorization") != "Bearer "+expected {
			t.Error("credential crossing")
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/session":
			var q struct{ Nonce string }
			json.NewDecoder(r.Body).Decode(&q)
			p := agentclient.Proof{AgentID: fmt.Sprintf("%x", sha256.Sum256(pub)), AgentPublicKey: base64.RawURLEncoding.EncodeToString(pub), EngineID: engine, Generation: 1, EngineKind: "docker", EngineSocket: socket, DaemonID: "daemon", Epoch: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Nonce: q.Nonce}
			msg, _ := agentclient.SessionBytes(p)
			p.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, msg))
			b, _ := json.Marshal(p)
			fmt.Fprintf(w, `{"version":"brine.agent.read/1","requestId":%q,"data":%s}`, r.Header.Get("X-Request-Id"), b)
		case "/v1/capabilities":
			if capsUnavailable.Load() {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if sourceOnlyCaps.Load() {
				fmt.Fprint(w, `{"schema":"dill.agent.capabilities.v1","monitor":["session","capabilities"],"deployment_grants":["journal","stop-preview","stage","manifest","preview"]}`)
				return
			}
			fmt.Fprint(w, `{"schema":"dill.agent.capabilities.v1","monitor":["session","capabilities"],"deployment_grants":["journal","stop-preview","stop"]}`)
		case "/v1/deploy/journal":
			w.Header().Set("X-Dill-Journal-Id", journal)
			fmt.Fprintf(w, `{"journalId":%q}`, journal)
		case "/v1/deploy/stop-preview":
			ownerPreviews++
			if ownerPreviews == replaceOnPreview {
				head = strings.Repeat("d", 32)
			}
			w.Header().Set("X-Dill-Journal-Id", journal)
			w.Header().Set("X-Dill-Prior-Request", head)
			w.Header().Set("X-Dill-Deployment-Mode", "stop")
			json.NewEncoder(w).Encode(plan)
		case "/v1/deploy/stop":
			calls++
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("X-Dill-Journal-Id", journal)
			if unknown {
				fmt.Fprintf(w, `{"requestId":%q,"stack":"demo","action":"stop","state":"unknown","effects":"unknown"}`, body["requestId"])
			} else {
				head = body["requestId"]
				fmt.Fprintf(w, `{"requestId":%q,"stack":"demo","action":"stop","state":"succeeded","effects":"complete","operationId":%q}`, head, "op_"+head)
			}
		default:
			t.Error("unexpected route", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer agent.Close()
	pin := sha256.Sum256(agent.Certificate().Raw)
	bundle := fmt.Sprintf(`{"version":1,"certificateSha256":%q,"token":%q,"agentPublicKey":%q,"engineId":%q,"generation":1,"engineKind":"docker","engineSocket":%q,"daemonId":"daemon"}`, hex.EncodeToString(pin[:]), monitor, hex.EncodeToString(pub), engine, socket)
	reg, e := targetregistry.Open(filepath.Join(t.TempDir(), "targets"))
	if e != nil {
		t.Fatal(e)
	}
	r := targetregistry.Record{ID: "demotarget", Revision: 1, HTTPSAddress: strings.TrimPrefix(agent.URL, "https://"), HTTPSPairing: targetregistry.PairingSecret(bundle)}
	if e = reg.AddContext(ctx, r, r); e != nil {
		t.Fatal(e)
	}
	data := t.TempDir()
	os.MkdirAll(filepath.Join(data, "stacks", "demo", "repo"), 0700)
	repository := filepath.Join(data, "stacks", "demo", "repo")
	os.WriteFile(filepath.Join(repository, "compose.pkl"), []byte("value = 42\n"), 0600)
	for _, args := range [][]string{{"init", "--initial-branch=main"}, {"add", "compose.pkl"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "synthetic"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repository
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture git %v: %s", err, out)
		}
	}
	fs, e := store.NewFSStore(data)
	if e != nil {
		t.Fatal(e)
	}
	s, e := fs.CreateFromGit("demo", &store.GitSource{URL: repository, Branch: "main"}, "compose.pkl")
	if e != nil {
		t.Fatal(e)
	}
	p := &store.RemoteGitPolicy{Enabled: true, Host: r.ID, Target: targetDigest(r), Source: s.RemoteGitSource(), Phase: "succeeded", Commit: strings.Repeat("c", 40), Seen: strings.Repeat("c", 40), DeploymentRequest: head, DeploymentJournal: journal}
	if e = fs.SwapRemoteGit("demo", p.Source, nil, p); e != nil {
		t.Fatal(e)
	}
	a, _ := auth.New(auth.Config{User: "admin", Password: "secret"})
	h, e := NewDeployment(a, reg, fs)
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	h.SetDeploymentCredentials(testDeploymentCredentials(deploy))
	gitMarker := filepath.Join(t.TempDir(), "git-called")
	gitBinary := filepath.Join(t.TempDir(), "git")
	if e = os.WriteFile(gitBinary, []byte(fmt.Sprintf("#!/bin/sh\nprintf hit >> %q\nexit 1\n", gitMarker)), 0700); e != nil {
		t.Fatal(e)
	}
	h.EnableGit(&gitpkg.Runner{Binary: gitBinary})
	// Permanent broker registration (or unreadable broker state) must deny all
	// unattended paths, including source-only work, before any target/Git call.
	guardCalls := 0
	h.SetLegacyGuard(func(context.Context, string, func()) error {
		guardCalls++
		return errors.New("broker protection unavailable")
	})
	if options, err := h.LifecycleTargets(ctx, "demo"); err == nil || len(options) != 0 {
		t.Fatal("broker-protected automation advertised", options, err)
	}
	before := func() bool { t.Fatal("broker denial reached dispatch"); return false }
	for _, action := range []string{"start", "stop", "restart"} {
		out, err := h.RunLifecycle(ctx, "demo", "binding", action, strings.Repeat("e", 32), before)
		if err == nil || out.Submitted {
			t.Fatal("broker lifecycle bypass", action, out, err)
		}
	}
	for _, action := range []string{"plan", "git-sync"} {
		out, err := h.RunSourceAction(ctx, "demo", "binding", action, before)
		if err == nil || out.Attempted {
			t.Fatal("broker source bypass", action, out, err)
		}
	}
	deniedDeploy := h.RunGitDeployment(ctx, "demo", "binding", strings.Repeat("e", 32), before)
	if deniedDeploy.Submitted || deniedDeploy.Error == nil {
		t.Fatal("broker incoming deploy bypass", deniedDeploy)
	}
	h.pollGitStack(ctx, "demo")
	if guardCalls != 8 || calls != 0 || ownerPreviews != 0 {
		t.Fatal("broker guard skipped", guardCalls, calls, ownerPreviews)
	}
	if _, err := os.Stat(gitMarker); !os.IsNotExist(err) {
		t.Fatal("broker-protected Git invoked", err)
	}
	freshDenied, err := fs.Get("demo")
	if err != nil || *freshDenied.RemoteGit != *p || !h.manualClear("demo") {
		t.Fatal("broker denial changed source/recovery", freshDenied, err)
	}
	h.SetLegacyGuard(nil)
	targets, e := h.LifecycleTargets(ctx, "demo")
	if e != nil || len(targets) != 1 {
		t.Fatal(targets, e)
	}
	if len(targets[0].Actions) != 2 || targets[0].Actions[0] != "git-sync" || targets[0].Actions[1] != "stop" {
		t.Fatal("ungranted lifecycle action advertised")
	}
	sourceOnlyCaps.Store(true)
	sourceTargets, err := h.LifecycleTargets(ctx, "demo")
	if err != nil || len(sourceTargets) != 1 || !slices.Contains(sourceTargets[0].Actions, "plan") || !slices.Contains(sourceTargets[0].Actions, "git-sync") || slices.Contains(sourceTargets[0].Actions, "stop") {
		t.Fatal("source actions required stop execution permission", sourceTargets, err)
	}
	savedGit := h.git
	h.git = nil
	noGit, err := h.LifecycleTargets(ctx, "demo")
	if err == nil || len(noGit) != 0 {
		t.Fatal("source actions advertised without runner", noGit, err)
	}
	h.git = savedGit
	sourceOnlyCaps.Store(false)
	capsUnavailable.Store(true)
	if opts, err := h.LifecycleTargets(ctx, "demo"); err == nil || len(opts) != 0 {
		t.Fatal("old capability-unavailable target offered automation")
	}
	capsUnavailable.Store(false)
	id := targets[0].ID
	// An external replacement on the same target is NOT the reviewed deployment.
	saved := head
	head = strings.Repeat("d", 32)
	out, e := h.RunLifecycle(ctx, "demo", id, "stop", strings.Repeat("e", 32), func() bool { return true })
	if e == nil || out.Submitted || calls != 0 {
		t.Fatal("unreviewed replacement mutated", out, e)
	}
	for _, action := range []string{"git-sync", "plan"} {
		beforePreview := ownerPreviews
		source, err := h.RunSourceAction(ctx, "demo", id, action, func() bool { t.Fatal("stale owner reached dispatch"); return false })
		if err == nil || source.Attempted || ownerPreviews <= beforePreview {
			t.Fatal("stale source owner not checked before capture/pull", action, source, err)
		}
		if _, err = os.Stat(gitMarker); !os.IsNotExist(err) {
			t.Fatal("stale source owner invoked git", action, err)
		}
	}
	head = saved
	h.git = gitpkg.New()
	for _, action := range []string{"git-sync", "plan"} {
		before := ownerPreviews
		replaceOnPreview = before + 2
		out, err := h.RunSourceAction(ctx, "demo", id, action, func() bool { t.Fatal("replacement after first owner check reached dispatch"); return false })
		if err == nil || out.Attempted || ownerPreviews != before+2 {
			t.Fatal("repeat live owner check missing", action, out, err, ownerPreviews-before)
		}
		fresh, _ := fs.Get("demo")
		if fresh.RemoteGit.Phase != "succeeded" || fresh.RemoteGit.SourceSync != "" {
			t.Fatal("replacement prepared source policy")
		}
		head = saved
		replaceOnPreview = 0
	}
	h.gitStore = failedSourceRollback{fs}
	source, sourceErr := h.RunSourceAction(ctx, "demo", id, "git-sync", func() bool { return false })
	blocked, _ := fs.Get("demo")
	if !errors.Is(sourceErr, ErrAutomationRecovery) || source.Attempted || blocked.RemoteGit.Phase != "blocked" || blocked.RemoteGit.SourceSync == "" {
		t.Fatal("source rollback failure hidden", source, sourceErr)
	}
	if e = fs.SwapRemoteGit("demo", p.Source, blocked.RemoteGit, p); e != nil {
		t.Fatal(e)
	}
	h.gitStore = fs
	h.stacks = failedAutomationResolution{fs}
	cleanupRequest := strings.Repeat("7", 32)
	denied, cleanupErr := h.RunLifecycle(ctx, "demo", id, "stop", cleanupRequest, func() bool { return false })
	if !errors.Is(cleanupErr, ErrAutomationRecovery) || denied.Submitted || h.manualClear("demo") {
		t.Fatal("failed preparation cleanup hidden", denied, cleanupErr)
	}
	intentToClear, err := fs.GetManualIntent(cleanupRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err = fs.ResolveManualIntent(intentToClear); err != nil {
		t.Fatal(err)
	}
	h.stacks = fs
	out, e = h.RunLifecycle(ctx, "demo", id, "stop", strings.Repeat("6", 32), func() bool {
		prepared, err := fs.GetManualIntent(strings.Repeat("6", 32))
		if err != nil || prepared.Resolved {
			t.Fatal("dispatch boundary preceded durable intent")
		}
		return false
	})
	if e == nil || out.Submitted || calls != 0 {
		t.Fatal("revoked dispatch admitted")
	}
	out, e = h.RunLifecycle(ctx, "demo", id, "stop", strings.Repeat("e", 32), func() bool { return true })
	if e != nil || !out.Submitted || out.Status.State != "succeeded" || calls != 1 {
		t.Fatal("known lifecycle", out, e, calls)
	}
	fresh, _ := fs.Get("demo")
	if fresh.RemoteGit.DeploymentRequest != head || !h.manualClear("demo") {
		t.Fatal("lost head or terminal recovery")
	}
	unknown = true
	out, e = h.RunLifecycle(ctx, "demo", id, "stop", strings.Repeat("f", 32), func() bool { return true })
	if e != nil || !out.Submitted || out.Status.State != "unknown" || h.manualClear("demo") {
		t.Fatal("unknown did not retain cross-path block", out, e)
	}
	out, e = h.RunLifecycle(ctx, "demo", id, "stop", strings.Repeat("1", 32), func() bool { return true })
	if e == nil || out.Submitted || calls != 2 {
		t.Fatal("unknown action replayed")
	}
	intent, e := fs.GetManualIntent(strings.Repeat("f", 32))
	if e != nil {
		t.Fatal(e)
	}
	terminal := agentclient.DeploymentStatus{RequestID: intent.Request, Stack: "demo", Action: "restart", State: "succeeded", Effects: "complete"}
	if h.reconcileLifecycleIntent(intent, terminal) {
		t.Fatal("foreign action reconciled source head")
	}
	terminal.Action = "stop"
	terminal.State = "failed"
	terminal.Effects = "partial"
	if h.reconcileLifecycleIntent(intent, terminal) {
		t.Fatal("partial effects released lifecycle inspection block")
	}
	terminal.State = "succeeded"
	terminal.Effects = "complete"
	if !h.reconcileLifecycleIntent(intent, terminal) {
		t.Fatal("authenticated inspected terminal could not reconcile saved predecessor")
	}
	if h.manualClear("demo") {
		t.Fatal("head reconciliation silently resolved inspection")
	}
	if fs.ResolveManualIntent(intent) != nil {
		t.Fatal("inspection resolution")
	}
	targets, e = h.LifecycleTargets(ctx, "demo")
	if e != nil || len(targets) != 1 || targets[0].ID != id {
		t.Fatal("verified recovery did not preserve scoped ceiling")
	}
}
