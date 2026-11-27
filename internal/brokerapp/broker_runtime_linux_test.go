//go:build linux

package brokerapp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/brokerwire"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/projectsnapshot"
	"github.com/umayr/brine/internal/secretbroker"
	"github.com/umayr/brine/internal/secrets"
)

// Opt-in only: uses a real designated Docker daemon. No cleanup/deletion, image
// pulls, arbitrary socket fallback, fake target or substituted provider is used.
func TestBrokerRuntimeLocalFile(t *testing.T) {
	root := os.Getenv("BROKER_TEST_ROOT")
	if os.Getenv("BRINE_BROKER_RUNTIME_TEST") != "1" || root == "" {
		t.Skip("explicit disposable runtime grant required")
	}
	if os.Geteuid() != 0 {
		t.Fatal("namespace UID0 required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	fixture, err := os.MkdirTemp(root, "file-local-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained fixture: %s", fixture)
	write := func(path string, b []byte) {
		t.Helper()
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(binary string, args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s failed: %v; metadata output: %s", filepath.Base(binary), args[0], err, out)
		}
		return out
	}
	nonce := make([]byte, 8)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	stack := "brokerq" + hex.EncodeToString(nonce)
	engine := "docker"
	cfg := dill.LocalBrokerConfig{Binary: filepath.Join(root, "dill"), Pkl: filepath.Join(root, "pkl"), CoordinationDir: filepath.Join(fixture, "coord"), RuntimeDir: filepath.Join(fixture, "runtime"), HistoryDir: filepath.Join(fixture, "history"), StagingRoot: filepath.Join(fixture, "stages"), CatalogRoot: filepath.Join(fixture, "catalog")}
	engineCLI := []string{"docker", "--host", "unix:///var/run/docker.sock"}
	if profile := os.Getenv("BROKER_TEST_PROFILE"); profile != "" {
		var pins struct {
			Engine   string `json:"engine"`
			Target   string `json:"targetDigest"`
			Endpoint string `json:"endpointDigest"`
		}
		if json.Unmarshal(run(cfg.Binary, "broker-profile-info", "--contract", "dill.broker-profile/1", "--broker-profile", profile), &pins) != nil {
			t.Fatal("operator profile export unavailable")
		}
		engine = pins.Engine
		cfg.ProfilePath = profile
		cfg.Engine = engine
		cfg.TargetDigest = pins.Target
		cfg.EndpointDigest = pins.Endpoint
		socket := os.Getenv("BROKER_TEST_SOCKET")
		if socket == "" {
			t.Fatal("independent test observation socket required")
		}
		if engine == "podman" {
			engineCLI = []string{"podman", "--remote", "--url", "unix://" + socket}
		} else {
			engineCLI = []string{"docker", "--host", "unix://" + socket}
		}
	} else {
		cfg.Socket = "/var/run/docker.sock"
		cfg.DaemonID = strings.TrimSpace(string(run("docker", "--host", "unix:///var/run/docker.sock", "info", "--format", "{{.ID}}")))
	}
	engineCommand := func(args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, engineCLI[0], append(append([]string{}, engineCLI[1:]...), args...)...)
	}
	for _, dir := range []string{cfg.CoordinationDir, cfg.StagingRoot, cfg.CatalogRoot} {
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	run(cfg.Binary, "broker-init", "--contract", brokerwire.Version, "--broker-store", cfg.RuntimeDir)
	if os.Getenv("BRINE_BROKER_RUNTIME_TRANSPORT") != "https" {
		args := []string{"broker-local", "init", "--contract", brokerwire.Version, "--coordination-state-dir", cfg.CoordinationDir, "--broker-store", cfg.RuntimeDir, "--history-dir", cfg.HistoryDir, "--staging-root", cfg.StagingRoot, "--staging-catalog", cfg.CatalogRoot, "--pkl", cfg.Pkl}
		if cfg.ProfilePath != "" {
			args = append(args, "--broker-profile", cfg.ProfilePath, "--engine-kind", cfg.Engine, "--target-digest", cfg.TargetDigest, "--endpoint-digest", cfg.EndpointDigest)
		} else {
			args = append(args, "--engine-socket", cfg.Socket, "--daemon-id", cfg.DaemonID)
		}
		run(cfg.Binary, args...)
	}
	value := make([]byte, 32)
	if _, err = rand.Read(value); err != nil {
		t.Fatal(err)
	}
	value = []byte(hex.EncodeToString(value))
	defer clear(value)
	secretFile := filepath.Join(fixture, "input-secret")
	write(secretFile, value)
	data := filepath.Join(fixture, "brine")
	if err = os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	if err = Initialize(data); err != nil {
		t.Fatal(err)
	}
	scope := secretbroker.Scope{RegistrationID: strings.Repeat("a", 32), StackName: stack, TargetRegistration: "local"}
	policy := Policy{Version: 1, Providers: secrets.Config{Version: 1, Connections: map[string]secrets.ConnectionConfig{"files": {Files: &secrets.FilesConfig{Sources: map[string]secrets.FileRef{"password": {Path: secretFile}}}}}}, Registrations: []Registration{{Scope: scope, Connections: []string{"files"}}}, Targets: map[string]TargetPolicy{"local": {Kind: "local", Local: &cfg, Grants: []string{"journal", "stage", "snapshot", "requirements", "prepare", "retire", "review", "apply", "status", "fence"}}}}
	binding := secretbroker.Binding{Name: "password", Kind: secretbroker.ProviderLocal, ConnectionID: "files", LocalAlias: "password"}
	if os.Getenv("BRINE_BROKER_RUNTIME_PROVIDER") == "vault" {
		policy.Providers.Connections["files"] = secrets.ConnectionConfig{Vault: runtimeVaultProvider(t, ctx, fixture, value)}
		binding = secretbroker.Binding{Name: "password", Kind: secretbroker.ProviderVault, ConnectionID: "files", Vault: secretbroker.VaultRef{Path: "password", Property: "value"}}
	}
	if os.Getenv("BRINE_BROKER_RUNTIME_TRANSPORT") == "https" {
		policy.Targets["local"] = runtimeHTTPSTargetPolicy(t, ctx, fixture, cfg)
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	policyFile := filepath.Join(data, "policy.json")
	write(policyFile, raw)
	service, err := Open(data, policyFile)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	revision, err := service.Save(scope, 0, "runtime-test", []secretbroker.Binding{binding})
	if err != nil {
		t.Fatal(err)
	}
	target, closeTarget, err := service.OpenTarget(ctx, "local", func(c context.Context) bool { return c.Err() == nil })
	if err != nil {
		t.Fatal(err)
	}
	defer closeTarget()
	local := target.(runtimeStager)
	schema, err := os.ReadFile(filepath.Join(root, "dill.pkl"))
	if err != nil {
		t.Fatal(err)
	}
	source := []byte("amends \"dill.pkl\"\nconfig { name = \"" + stack + "\"\nengine = \"" + engine + "\"\nsecrets { new { name = \"password\" } }\nservices { [\"web\"] { image = \"nginx:alpine\"; pullPolicy = \"never\"; user = \"0:0\"; command = List(\"sleep\",\"3600\"); secrets { new { name = \"password\"; target = \"/run/secrets/password\"; uid = 0; gid = 0; mode = 256 } } } } }\n")
	snap := projectsnapshot.Snapshot{Entrypoint: "compose.pkl", Files: []projectsnapshot.File{{Path: "compose.pkl", Content: source, SHA256: sha256.Sum256(source)}, {Path: "dill.pkl", Content: schema, SHA256: sha256.Sum256(schema)}}}
	files := []any{}
	for _, f := range snap.Files {
		files = append(files, map[string]any{"path": f.Path, "bytes": f.Content, "length": len(f.Content), "sha256": hex.EncodeToString(f.SHA256[:])})
	}
	upload, err := json.Marshal(map[string]any{"schema": "dill.snapshot.v1", "entrypoint": snap.Entrypoint, "files": files})
	if err != nil {
		t.Fatal(err)
	}
	stage, err := local.Stage(ctx, upload)
	if err != nil {
		t.Fatal("stage", err)
	}
	manifest, err := local.Snapshot(ctx, stage)
	if err != nil {
		t.Fatal("manifest", err)
	}
	expected, err := dillserve.SnapshotManifestDigest(ctx, snap)
	if err != nil || manifest.Digest() != expected {
		t.Fatal("source commitment mismatch", err)
	}
	info, err := target.Info(ctx)
	if err != nil {
		t.Fatal("journal", err)
	}
	requirements, reqErr := target.Requirements(ctx, info.Journal, stage, hex.EncodeToString(expected[:]), stack)
	if reqErr != nil {
		t.Fatal("requirements discovery", reqErr)
	}
	t.Logf("real requirements valid=%v names=%d grants=%d", requirements.Valid(), len(requirements.Names), len(requirements.Grants))
	identity := secretbroker.ReviewIdentity{Scope: scope, SourceDigest: "sha256:" + hex.EncodeToString(expected[:]), TargetDigest: info.TargetDigest, PolicyRevision: service.Revision(), SessionDigest: brokerwire.Digest([]byte("runtime-test-session")), BindingRevision: revision}
	controller, err := NewController(service, func(c context.Context, i secretbroker.ReviewIdentity) (func(), error) {
		if c.Err() != nil || i != identity {
			return nil, ErrUnavailable
		}
		return func() {}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	target = runtimeTraceTarget{Target: target, t: t, referenceFile: filepath.Join(fixture, "prepared-reference.json")}
	review, err := controller.Prepare(ctx, identity, stage, hex.EncodeToString(expected[:]), target)
	if err != nil {
		t.Fatal("native Plan", err)
	}
	record, err := controller.Submit(ctx, review.ID, identity, target)
	if err != nil {
		t.Fatalf("apply failed/unknown; retained request=%s events=%+v err=%v", record.Intent.ID, record.Events, err)
	}
	got, err := engineCommand("exec", stack+"_web", "cat", "/run/secrets/password").Output()
	if err != nil || string(got) != string(value) {
		clear(got)
		t.Fatal("actual container did not consume exact provider file")
	}
	clear(got)
	if _, err = controller.Submit(ctx, review.ID, identity, target); err == nil {
		t.Fatal("consumed review replayed")
	}
	if service.RequireUnprotected(stack) == nil {
		t.Fatal("ordinary fallback permitted")
	}
	container := stack + "_web"
	if err = engineCommand("exec", "--user", "65534:65534", container, "cat", "/run/secrets/password").Run(); err == nil {
		t.Fatal("unauthorized UID read secret")
	}
	if err = engineCommand("exec", container, "sh", "-c", "printf write-probe > /run/secrets/password").Run(); err == nil {
		t.Fatal("secret mount was writable")
	}
	for _, f := range []string{filepath.Join(cfg.HistoryDir, "operations.json"), filepath.Join(data, "broker-state", "references.db")} {
		b, e := os.ReadFile(f)
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(string(b), string(value)) {
			clear(b)
			t.Fatal("value leaked to immutable history")
		}
		clear(b)
	}
	before, e := os.ReadFile(secretFile)
	if e != nil || string(before) != string(value) {
		clear(before)
		t.Fatal("provider source changed")
	}
	clear(before)
	t.Logf("PASS provider=%s transport=%s -> Brine policy/controller -> reviewed %s consumer; stack=%s request=%s", binding.Kind, policy.Targets["local"].Kind, engine, stack, record.Intent.ID)

	// Inject response loss AFTER a real target apply, never a fake engine result.
	// Recovery must query that exact durable request, including after app restart.
	review, err = controller.Prepare(ctx, identity, stage, hex.EncodeToString(expected[:]), target)
	if err != nil {
		t.Fatal("update Plan", err)
	}
	calls := 0
	unknown, submitErr := controller.Submit(ctx, review.ID, identity, lostRuntimeReplyTarget{Target: target, calls: &calls})
	if submitErr == nil || unknown.Intent.ID == "" || len(unknown.Events) == 0 || unknown.Events[len(unknown.Events)-1].State != "unknown" {
		t.Fatal("lost target reply did not preserve unknown")
	}
	if _, err = controller.Submit(ctx, review.ID, identity, lostRuntimeReplyTarget{Target: target, calls: &calls}); err == nil || calls != 1 {
		t.Fatal("ambiguous apply replayed")
	}
	if _, err = controller.Prepare(ctx, identity, stage, hex.EncodeToString(expected[:]), target); err == nil {
		t.Fatal("unknown request did not block new prepare")
	}
	guard := controller.guard
	service.Close()
	service, err = Open(data, policyFile)
	if err != nil {
		t.Fatal("application restart", err)
	}
	defer service.Close()
	target, closeTarget, err = service.OpenTarget(ctx, "local", func(c context.Context) bool { return c.Err() == nil })
	if err != nil {
		t.Fatal(err)
	}
	defer closeTarget()
	local = target.(runtimeStager)
	target = runtimeTraceTarget{Target: target, t: t, referenceFile: filepath.Join(fixture, "prepared-reference.json")}
	controller, err = NewController(service, guard)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := controller.Reconcile(ctx, unknown, target, func(context.Context, secretbroker.Scope) error { return nil })
	if err != nil || recovered.State != "succeeded" {
		t.Fatal("same-request authoritative restart reconciliation", err)
	}
	t.Logf("PASS real update with injected reply loss, no replay, app restart and authoritative status; request=%s", unknown.Intent.ID)

	// Retirement is a new explicitly captured source revision and empty binding
	// revision; no projection into ordinary execution and no generation cleanup.
	retiredSource := strings.Replace(string(source), "secrets { new { name = \"password\" } }\n", "", 1)
	retiredSource = strings.Replace(retiredSource, "secrets { new { name = \"password\"; target = \"/run/secrets/password\"; uid = 0; gid = 0; mode = 256 } }", "", 1)
	retired := []byte(retiredSource)
	snap.Files[0].Content = retired
	snap.Files[0].SHA256 = sha256.Sum256(retired)
	files = []any{}
	for _, f := range snap.Files {
		files = append(files, map[string]any{"path": f.Path, "bytes": f.Content, "length": len(f.Content), "sha256": hex.EncodeToString(f.SHA256[:])})
	}
	upload, err = json.Marshal(map[string]any{"schema": "dill.snapshot.v1", "entrypoint": snap.Entrypoint, "files": files})
	if err != nil {
		t.Fatal(err)
	}
	stage, err = local.Stage(ctx, upload)
	if err != nil {
		t.Fatal("retirement source stage", err)
	}
	manifest, err = local.Snapshot(ctx, stage)
	if err != nil {
		t.Fatal(err)
	}
	expected = manifest.Digest()
	revision, err = service.Save(scope, revision, "runtime-test", []secretbroker.Binding{})
	if err != nil {
		t.Fatal(err)
	}
	identity.SourceDigest = "sha256:" + hex.EncodeToString(expected[:])
	identity.BindingRevision = revision
	review, err = controller.Prepare(ctx, identity, stage, hex.EncodeToString(expected[:]), target)
	if err != nil {
		t.Fatal("retirement Plan", err)
	}
	if review.Retirement == nil {
		t.Fatal("empty declarations did not select explicit retirement")
	}
	record, err = controller.Submit(ctx, review.ID, identity, target)
	if err != nil {
		t.Fatalf("retirement outcome unavailable; request=%s events=%+v", record.Intent.ID, record.Events)
	}
	if err = engineCommand("exec", container, "test", "!", "-e", "/run/secrets/password").Run(); err != nil {
		t.Fatal("retirement retained mounted secret")
	}
	if service.RequireUnprotected(stack) == nil {
		t.Fatal("retirement removed permanent protection")
	}
	t.Logf("PASS read-only/unauthorized UID denial/history non-disclosure and real reviewed retirement; stack=%s request=%s", stack, record.Intent.ID)
}

type lostRuntimeReplyTarget struct {
	Target
	calls *int
}

func (t lostRuntimeReplyTarget) Apply(c context.Context, j, s, id, digest, mode string, r brokerwire.Reference) (Outcome, error) {
	(*t.calls)++
	_, err := t.Target.Apply(c, j, s, id, digest, mode, r)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{}, ErrUnavailable
}

type runtimeTraceTarget struct {
	Target
	t             *testing.T
	referenceFile string
}

func (t runtimeTraceTarget) Prepare(c context.Context, j, s string, e brokerwire.Envelope) (brokerwire.Review, error) {
	r, err := t.Target.Prepare(c, j, s, e)
	t.t.Logf("real target prepare: %v", err)
	if err == nil {
		raw, e := json.Marshal(brokerwire.Reference{Scope: r.Scope, Handle: r.Handle})
		if e == nil {
			e = os.WriteFile(t.referenceFile, raw, 0600)
		}
		if e != nil {
			t.t.Fatal("metadata evidence write failed", e)
		}
	}
	return r, err
}
func (t runtimeTraceTarget) Review(c context.Context, j, s string, r brokerwire.Reference) (*dill.BrokerReview, string, error) {
	v, m, err := t.Target.Review(c, j, s, r)
	t.t.Logf("real target review: %v", err)
	return v, m, err
}
func (t runtimeTraceTarget) Engine() string { return t.Target.(interface{ Engine() string }).Engine() }
func (t runtimeTraceTarget) Retire(c context.Context, j, s string, r brokerwire.Retirement) (brokerwire.Review, error) {
	v, err := t.Target.(interface {
		Retire(context.Context, string, string, brokerwire.Retirement) (brokerwire.Review, error)
	}).Retire(c, j, s, r)
	t.t.Logf("real target retirement prepare: %v", err)
	return v, err
}
