package web

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/brokerapp"
	"github.com/umayr/brine/internal/brokerwire"
	"github.com/umayr/brine/internal/configvars"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/secretbroker"
	"github.com/umayr/brine/internal/secrets"
	"github.com/umayr/brine/internal/store"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type workflowAcquire struct{}

func (workflowAcquire) Acquire(context.Context, secrets.Selector) (secrets.Value, error) {
	return secrets.NewValue([]byte("synthetic-workflow-value"))
}

type workflowTarget struct {
	t            *testing.T
	state        *secretbroker.Store
	scope        secretbroker.Scope
	manifest     dillserve.Manifest
	writes       int
	intentExists func(string) bool
	changed      bool
	retirement   bool
	engine       string
}

func (t *workflowTarget) Engine() string {
	if t.engine != "" {
		return t.engine
	}
	return "docker"
}
func (t *workflowTarget) Info(context.Context) (brokerapp.TargetInfo, error) {
	target := "target"
	if t.changed {
		target = "changed"
	}
	return brokerapp.TargetInfo{Journal: strings.Repeat("c", 32), TargetDigest: brokerwire.Digest([]byte(target)), EndpointDigest: brokerwire.Digest([]byte("endpoint"))}, nil
}
func (t *workflowTarget) Stage(ctx context.Context, b []byte) (string, error) {
	var w struct {
		Entrypoint string `json:"entrypoint"`
		Files      []struct {
			Path   string `json:"path"`
			Length int    `json:"length"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return "", err
	}
	m, _ := json.Marshal(struct {
		Schema     string `json:"schema"`
		Entrypoint string `json:"entrypoint"`
		Files      any    `json:"files"`
	}{"dill.snapshot.manifest.v1", w.Entrypoint, w.Files})
	sum := sha256.Sum256(m)
	id := strings.Repeat("a", 32)
	raw, _ := json.Marshal(map[string]any{"stage_id": id, "manifest": json.RawMessage(m), "manifest_sha256": hex.EncodeToString(sum[:])})
	var err error
	t.manifest, err = dillserve.ParseCatalog(ctx, id, raw)
	return id, err
}
func (t *workflowTarget) Snapshot(context.Context, string) (dillserve.Manifest, error) {
	return t.manifest, nil
}
func (t *workflowTarget) Requirements(context.Context, string, string, string, string) (brokerwire.Requirements, error) {
	if t.retirement {
		return brokerwire.Requirements{Contract: brokerwire.Version, Stack: "demo", Engine: t.Engine(), ConfigDigest: brokerwire.Digest([]byte("empty-config")), Names: []string{}, Grants: []brokerwire.Grant{}}, nil
	}
	return brokerwire.Requirements{Contract: brokerwire.Version, Stack: "demo", Engine: t.Engine(), ConfigDigest: brokerwire.Digest([]byte("config")), Names: []string{"password"}, Grants: []brokerwire.Grant{{Service: "web", Name: "password", Target: "/run/secrets/password", Mode: 0400}}}, nil
}
func (t *workflowTarget) Prepare(_ context.Context, _ string, _ string, e brokerwire.Envelope) (brokerwire.Review, error) {
	if len(e.Items) != 1 || string(e.Items[0].Bytes()) != "synthetic-workflow-value" {
		t.t.Fatal("frozen bytes mismatch")
	}
	return brokerwire.Review{Scope: e.Scope, Handle: strings.Repeat("b", 32), ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (t *workflowTarget) Review(_ context.Context, _ string, _ string, r brokerwire.Reference) (*dill.BrokerReview, string, error) {
	d := brokerwire.Digest([]byte("selection"))
	return &dill.BrokerReview{Reference: r, Preflight: &dill.PreflightResult{SelectionDigest: &d}, Plan: &dill.BrokerPlan{Changes: []dill.PlanChange{{Service: "web", Kind: "create"}}}}, "create", nil
}
func (t *workflowTarget) Apply(_ context.Context, _ string, _ string, id, _, _ string, ref brokerwire.Reference) (brokerapp.Outcome, error) {
	if ref.Scope.Engine != t.Engine() {
		t.t.Fatal("target engine changed at dispatch")
	}
	if t.intentExists == nil || !t.intentExists(id) {
		t.t.Fatal("dispatch without durable intent")
	}
	t.writes++
	return brokerapp.Outcome{}, brokerapp.ErrUnavailable
}
func (t *workflowTarget) Status(_ context.Context, _ string, id string, _ brokerwire.Reference) (brokerapp.Outcome, error) {
	return brokerapp.Outcome{RequestID: id, Stack: "demo", State: "unknown", Effects: "unknown"}, nil
}
func TestBrokerAuthenticatedHTTPWorkflow(t *testing.T) {
	scenarios := []string{"podman", "retirement", "retirement-source", "retirement-variables", "retirement-bindings", "retirement-session", "retirement-target", "retirement-policy", "unknown", "git-folder", "git-folder-drift", "remote-disabled", "remote-enabled", "remote-unknown", "source", "variables", "bindings", "session", "target", "policy", "csrf", "value"}
	if os.Getenv("BRINE_BROKER_BROWSER_MODULE") != "" {
		scenarios = append(scenarios, "browser-dark-1440", "browser-dark-390", "browser-light-1440", "browser-light-390", "browser-dark-390-retirement", "browser-light-1440-retirement", "browser-dark-1440-podman", "browser-light-390-podman")
	}
	for _, scenario := range scenarios {
		t.Run(scenario, func(t *testing.T) {
			data, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Chmod(filepath.Dir(data), 0700); err != nil {
				t.Fatal(err)
			}
			if err = brokerapp.Initialize(data); err != nil {
				t.Fatal(err)
			}
			st, err := store.NewFSStore(data)
			if err != nil {
				t.Fatal(err)
			}
			var stack *store.Stack
			gitFolder := strings.HasPrefix(scenario, "git-folder") || strings.HasPrefix(scenario, "remote-") || strings.HasPrefix(scenario, "browser-")
			if gitFolder {
				repo, e := st.RepoDir("demo")
				if e != nil {
					t.Fatal(e)
				}
				root := filepath.Join(repo, "apps", "db")
				folder := "apps/db"
				if strings.HasPrefix(scenario, "remote-") {
					root = repo
					folder = ""
				}
				if e = os.MkdirAll(root, 0700); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(root, "compose.pkl"), []byte("config {}\n"), 0600); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(repo, "excluded.txt"), []byte("not in project"), 0600); e != nil {
					t.Fatal(e)
				}
				stack, err = st.CreateFromGit("demo", &store.GitSource{URL: "https://example.invalid/synthetic.git", Folder: folder}, "compose.pkl")
			} else {
				stack, err = st.Create("demo", "config {}\n")
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(scenario, "remote-") {
				p := store.RemoteGitPolicy{Enabled: scenario == "remote-enabled", Host: "host", Source: stack.RemoteGitSource(), Target: strings.Repeat("a", 64), Phase: "ready"}
				if scenario == "remote-unknown" {
					p.Commit = strings.Repeat("b", 40)
					p.Seen = p.Commit
					p.Request = strings.Repeat("c", 32)
					p.Journal = strings.Repeat("d", 32)
					p.Phase = "pending"
					p.Reason = "unknown"
				}
				if err = st.SwapRemoteGit("demo", p.Source, nil, &p); err != nil {
					t.Fatal(err)
				}
			}
			vars, err := configvars.New(data, st)
			if err != nil {
				t.Fatal(err)
			}
			if err = vars.Set("demo", "DOMAIN", "captured-domain", false); err != nil {
				t.Fatal(err)
			}
			run := dill.New("/never-executed")
			run.Variables = vars
			scope := secretbroker.Scope{RegistrationID: strings.Repeat("a", 32), StackName: "demo", TargetRegistration: "local"}
			policy := brokerapp.Policy{Version: 1, Providers: secrets.Config{Version: 1, Connections: map[string]secrets.ConnectionConfig{"files": {Files: &secrets.FilesConfig{Sources: map[string]secrets.FileRef{"password": {Path: "/not-read"}}}}}}, Registrations: []brokerapp.Registration{{Scope: scope, Connections: []string{"files"}}}}
			raw, _ := json.Marshal(policy)
			path := filepath.Join(data, "policy.json")
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			svc, err := brokerapp.Open(data, path)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			if _, err = svc.Save(scope, 0, "admin", []secretbroker.Binding{{Name: "password", Kind: secretbroker.ProviderLocal, ConnectionID: "files", LocalAlias: "password"}}); err != nil {
				t.Fatal(err)
			}
			svc.Manager.Close()
			svc.Manager, err = secretbroker.NewManager(svc.State, workflowAcquire{}, func(_ context.Context, i secretbroker.ReviewIdentity, b []secretbroker.Binding) error {
				if i.PolicyRevision != svc.Revision() {
					return brokerapp.ErrUnavailable
				}
				return svc.Allowed(i.Scope, b)
			})
			if err != nil {
				t.Fatal(err)
			}
			authn, err := auth.New(auth.Config{User: "admin", Password: "test-only-password"})
			if err != nil {
				t.Fatal(err)
			}
			h := New(agent.NewLocalAgent(nil, run), st, nil, &git.Runner{Binary: "/never-executed-git"}, "docker", false, authn, time.Minute)
			h.EnableBroker(svc)
			reader, err := sql.Open("sqlite", "file:"+filepath.Join(data, "broker-state", "references.db")+"?mode=ro")
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			target := &workflowTarget{t: t, state: svc.State, scope: scope, intentExists: func(id string) bool {
				var n int
				return reader.QueryRow("SELECT count(*) FROM broker_deployments WHERE id=?", id).Scan(&n) == nil && n == 1
			}}
			if strings.Contains(scenario, "podman") {
				target.engine = "podman"
			}
			if strings.Contains(scenario, "retirement") {
				target.retirement = true
				seedWorkflowRetirement(t, svc, scope)
			}
			h.brokerTargets = func(ctx context.Context, id string, current func(context.Context) bool) (brokerapp.Target, func(), error) {
				if id != "local" || !current(ctx) {
					return nil, nil, brokerapp.ErrUnavailable
				}
				return target, func() {}, nil
			}
			router := chi.NewRouter()
			router.Use(authn.Middleware)
			Mount(router, h, httpx.Timeouts{})
			login := func() *http.Cookie {
				r := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=test-only-password"))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				w := httptest.NewRecorder()
				authn.HandleLogin(w, r)
				if w.Code != 303 {
					t.Fatal("login", w.Code)
				}
				return w.Result().Cookies()[0]
			}
			cookie := login()
			request := func(method, path string, v url.Values) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, strings.NewReader(v.Encode()))
				r.AddCookie(cookie)
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				if strings.Contains(w.Body.String(), "synthetic-workflow-value") {
					t.Fatal("value leaked")
				}
				return w
			}
			field := func(body, name string) string {
				m := regexp.MustCompile(`name="` + name + `" value="([^"]+)"`).FindStringSubmatch(body)
				if len(m) != 2 {
					t.Fatalf("missing %s: %s", name, body)
				}
				return m[1]
			}
			if strings.HasPrefix(scenario, "browser-") {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/login" {
						authn.HandleLogin(w, r)
						return
					}
					router.ServeHTTP(w, r)
				}))
				defer server.Close()
				parts := strings.Split(scenario, "-")
				out, err := filepath.Abs("../../.local/secrets-implementation/browser")
				if err != nil {
					t.Fatal(err)
				}
				if err = os.MkdirAll(out, 0700); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command("node", "../../scripts/verify-broker-workflow.cjs", server.URL, parts[1], parts[2], filepath.Join(out, scenario), fmt.Sprint(target.retirement))
				b, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("browser: %v\n%s", err, b)
				}
				t.Log(string(b))
				if target.writes != 1 {
					t.Fatal("browser replayed or did not submit")
				}
				return
			}
			intro := request("GET", "/web/stacks/demo/plan", nil)
			if scenario == "remote-enabled" || scenario == "remote-unknown" {
				if !strings.Contains(intro.Body.String(), "Source capture unavailable") || target.writes != 0 {
					t.Fatal("active/pending automation ignored")
				}
				return
			}
			if !strings.Contains(intro.Body.String(), "BRINE_VAR_DOMAIN") || strings.Contains(intro.Body.String(), "captured-domain") {
				t.Fatal("variable consent missing or value displayed")
			}
			if intro.Code != 200 {
				t.Fatal(intro.Code)
			}
			csrf := field(intro.Body.String(), "_csrf")
			source := field(intro.Body.String(), "source")
			prepare := url.Values{"_csrf": {csrf}, "source": {source}, "registration": {scope.RegistrationID}, "confirm": {"prepare"}}
			if scenario == "csrf" {
				prepare.Del("_csrf")
			} else if scenario == "value" {
				prepare.Set("value", "untrusted")
			}
			review := request("POST", "/web/stacks/demo/broker/prepare", prepare)
			if scenario == "csrf" || scenario == "value" {
				want := 400
				if scenario == "csrf" {
					want = 403
				}
				if review.Code != want || target.writes != 0 {
					t.Fatal("invalid request admitted", review.Code)
				}
				return
			}
			if review.Code != 200 || !strings.Contains(review.Body.String(), "Deploy reviewed Plan") || (!target.retirement && !strings.Contains(review.Body.String(), "/run/secrets/password")) || (target.retirement && !strings.Contains(review.Body.String(), "Review secret retirement")) {
				t.Fatalf("native review %d: %s", review.Code, review.Body.String())
			}
			id := field(review.Body.String(), "review")
			switch strings.TrimPrefix(scenario, "retirement-") {
			case "git-folder-drift":
				repo, e := st.RepoDir("demo")
				if e != nil {
					t.Fatal(e)
				}
				other := filepath.Join(repo, "apps", "other")
				if e = os.MkdirAll(other, 0700); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(other, "compose.pkl"), []byte("config {}\n"), 0600); e != nil {
					t.Fatal(e)
				}
				err = st.UpdateGitSource("demo", &store.GitSource{URL: "https://example.invalid/synthetic.git", Folder: "apps/other"})
			case "source":
				err = os.WriteFile(stack.PklPath, []byte("changed config\n"), 0600)
			case "variables":
				err = vars.Set("demo", "DOMAIN", "changed-domain", false)
			case "bindings":
				_, err = svc.State.Save(scope, map[bool]int64{false: 1, true: 2}[target.retirement], "admin", nil)
			case "session":
				cookie = login()
			case "target":
				target.changed = true
			case "policy":
				svc.Manager.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			confirm := url.Values{"_csrf": {csrf}, "review": {id}, "confirm": {"deploy"}}
			if strings.TrimPrefix(scenario, "retirement-") == "session" {
				newIntro := request("GET", "/web/stacks/demo/plan", nil)
				confirm.Set("_csrf", field(newIntro.Body.String(), "_csrf"))
			}
			applied := request("POST", "/web/stacks/demo/broker/confirm", confirm)
			if scenario != "unknown" && scenario != "git-folder" && scenario != "remote-disabled" && scenario != "retirement" && scenario != "podman" {
				if applied.Code != 409 || target.writes != 0 {
					t.Fatalf("stale %s admitted: %d writes=%d %s", scenario, applied.Code, target.writes, applied.Body.String())
				}
				return
			}
			if applied.Code != 200 || target.writes != 1 || !strings.Contains(applied.Body.String(), "not confirmed successful") {
				t.Fatal("ambiguous apply", applied.Code, applied.Body.String())
			}
			again := request("POST", "/web/stacks/demo/broker/confirm", confirm)
			if again.Code != 409 || target.writes != 1 {
				t.Fatal("replay")
			}
			rows, err := svc.State.Deployments(scope)
			if err != nil || len(rows) != map[bool]int{false: 1, true: 2}[target.retirement] || rows[0].Events[len(rows[0].Events)-1].State != "unknown" {
				t.Fatal("durable unknown", err)
			}
			lookup := request("POST", "/web/stacks/demo/broker/status", url.Values{"_csrf": {csrf}, "request": {rows[0].Intent.ID}, "registration": {scope.RegistrationID}})
			if lookup.Code != 200 || !strings.Contains(lookup.Body.String(), "unknown") || target.writes != 1 {
				t.Fatal("lookup replay or outcome inferred", lookup.Code)
			}
		})
	}
}
