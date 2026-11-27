package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/umayr/brine/internal/agentclient"
	gitpkg "github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/remoteui"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/targetregistry"
	"github.com/umayr/brine/internal/web"
	"github.com/umayr/brine/internal/webhooks"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Explicit opt-in: this mutates ONLY the disposable daemon supplied by the caller.
func TestSignedIncomingRemoteLifecycle(t *testing.T) {
	if os.Getenv("BRINE_REAL_INCOMING_LIFECYCLE") != "1" {
		t.Skip("requires explicit disposable Linux runtime qualification")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("native Linux required")
	}
	bin, pkl, schema, socket := os.Getenv("DILL_AGENT_HTTPS_BIN"), os.Getenv("DILL_PKL_PATH"), os.Getenv("BRINE_TEST_DILL_SCHEMA"), os.Getenv("DILL_QUAL_SOCKET")
	if bin == "" || pkl == "" || schema == "" || socket == "" {
		t.Fatal("explicit binary, Pkl, schema and disposable socket required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	engine := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}, Timeout: 10 * time.Second}
	defer engine.CloseIdleConnections()
	engineCall := func(method, path string) []byte {
		t.Helper()
		r, _ := http.NewRequestWithContext(ctx, method, "http://docker"+path, nil)
		resp, e := engine.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 300 {
			t.Fatalf("engine %s %s: %d %s", method, path, resp.StatusCode, b)
		}
		return b
	}
	var info struct{ ID string }
	if json.Unmarshal(engineCall("GET", "/info"), &info) != nil || info.ID == "" {
		t.Fatal("daemon identity unavailable")
	}
	stack := fmt.Sprintf("brine-it-lifecycle-%d", time.Now().UnixNano())
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	credentials := filepath.Join(dir, "credentials")
	command := func(args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, bin, append([]string{"agent"}, args...)...)
	}
	if out, e := command("init-https", "--dir", credentials).CombinedOutput(); e != nil {
		t.Fatalf("init: %v %s", e, out)
	}
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{19}, 32))
	tokenFile := filepath.Join(dir, "deployment-token")
	os.WriteFile(tokenFile, []byte(token), 0600)
	for _, name := range []string{"stages", "catalog", "coordination", "history"} {
		os.Mkdir(filepath.Join(dir, name), 0700)
	}
	reserve, _ := net.Listen("tcp", "127.0.0.1:0")
	address := reserve.Addr().String()
	args := []string{"--https-listen", address, "--https-dir", credentials, "--engine-socket", socket, "--engine-id", "11111111-1111-4111-8111-111111111111", "--generation", "1", "--daemon-id", info.ID, "--https-deploy-token-file", tokenFile, "--https-staging-root", filepath.Join(dir, "stages"), "--https-staging-catalog", filepath.Join(dir, "catalog"), "--https-preview-state", filepath.Join(dir, "coordination"), "--https-preview-pkl", pkl, "--https-apply-state", filepath.Join(dir, "history")}
	for _, grant := range []string{"stage", "manifest", "preview", "journal", "journal-apply", "start-preview", "start", "stop-preview", "stop", "restart-preview", "restart", "status", "fence"} {
		args = append(args, "--https-deploy-grant", grant)
	}
	raw, e := command(append(args, "--print-https-pairing")...).Output()
	if e != nil {
		reserve.Close()
		t.Fatal("pair export", e)
	}
	pair, e := agentclient.ParsePairing(raw)
	if e != nil {
		t.Fatal(e)
	}
	reserve.Close()
	process := command(args...)
	var logs bytes.Buffer
	process.Stderr = &logs
	if e = process.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { process.Process.Kill(); process.Wait() }()
	var c *agentclient.Client
	var epoch string
	for ctx.Err() == nil {
		c, epoch, e = pair.OpenDeploymentCurrent(ctx, ctx, address, token, func(context.Context) bool { return true })
		if e == nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(30 * time.Millisecond):
		}
	}
	if e != nil {
		t.Fatal("open agent", e, logs.String())
	}
	defer c.Close()
	schemaBytes, e := os.ReadFile(schema)
	if e != nil {
		t.Fatal(e)
	}
	compose := []byte(fmt.Sprintf(`amends "dill.pkl"
config {
 name = %q
 engine = "docker"
 services {
  ["app"] {
   image = "docker.io/library/busybox:1.36"
   command = List("sh", "-c", "trap 'exit 0' TERM; while true; do sleep 1; done")
  }
 }
}
`, stack))
	files := []map[string]any{}
	for name, content := range map[string][]byte{"compose.pkl": compose, "dill.pkl": schemaBytes} {
		files = append(files, map[string]any{"path": name, "bytes": base64.StdEncoding.EncodeToString(content), "length": len(content), "sha256": fmt.Sprintf("%x", sha256.Sum256(content))})
	}
	body, _ := json.Marshal(map[string]any{"schema": "dill.snapshot.v1", "entrypoint": "compose.pkl", "files": files})
	stage, e := c.Stage(ctx, epoch, body)
	if e != nil {
		t.Fatal("stage", e)
	}
	manifest, e := c.Snapshot(ctx, epoch, stage)
	if e != nil {
		t.Fatal(e)
	}
	digest := fmt.Sprintf("%x", manifest.Digest())
	preview, e := c.PreviewDeployment(ctx, epoch, stage, digest, stack)
	if e != nil {
		t.Fatal("create preview", e, logs.String())
	}
	if !preview.CreateEnabled || preview.JournalID == "" || preview.SelectionDigest == nil {
		t.Fatal("unqualified creation preview")
	}
	discovered, err := c.DeploymentJournal(ctx, epoch)
	if err != nil || discovered != preview.JournalID {
		t.Fatal("separately granted current journal discovery", err)
	}
	id := fmt.Sprintf("%032x", 1)
	result, e := c.ApplyJournalDeployment(ctx, epoch, stage, digest, stack, *preview.SelectionDigest, id, "create", preview.JournalID)
	if e != nil || result.State != "succeeded" {
		t.Fatal("create", e, result, logs.String())
	}
	createdIDs := []string{}
	cleanupNetwork := ""
	// Pin cleanup immediately after successful creation. A later object with
	// the same stack/network name is not this test's cleanup authority.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		paths := []string{}
		for _, id := range createdIDs {
			paths = append(paths, "/containers/"+id+"?force=1")
		}
		if cleanupNetwork != "" {
			paths = append(paths, "/networks/"+cleanupNetwork)
		}
		for _, path := range paths {
			req, _ := http.NewRequestWithContext(cleanup, "DELETE", "http://docker"+path, nil)
			resp, err := engine.Do(req)
			if err != nil {
				t.Errorf("fixture cleanup %s: %v", path, err)
				continue
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode >= 300 {
				t.Errorf("fixture cleanup %s HTTP%d", path, resp.StatusCode)
			}
		}
	}()
	var containers []struct {
		ID     string `json:"Id"`
		Labels map[string]string
	}
	if json.Unmarshal(engineCall("GET", "/containers/json?all=1"), &containers) != nil {
		t.Fatal("fixture inventory")
	}
	for _, item := range containers {
		if item.Labels["dill.stack"] == stack && item.Labels["dill.managed"] == "true" {
			createdIDs = append(createdIDs, item.ID)
		}
	}
	var network struct {
		ID   string `json:"Id"`
		Name string
	}
	if json.Unmarshal(engineCall("GET", "/networks/"+stack), &network) != nil || network.Name != stack || network.ID == "" || len(createdIDs) != 1 {
		t.Fatal("fixture identities unavailable")
	}
	cleanupNetwork = network.ID
	inspect := func() (bool, string) {
		t.Helper()
		var item struct {
			ID    string `json:"Id"`
			State struct {
				Running   bool
				StartedAt string
			}
		}
		if json.Unmarshal(engineCall("GET", "/containers/"+createdIDs[0]+"/json"), &item) != nil || item.ID != createdIDs[0] || item.State.StartedAt == "" {
			t.Fatal("fixture inspection unavailable")
		}
		return item.State.Running, item.State.StartedAt
	}
	running, started := inspect()
	if !running {
		t.Fatal("creation did not produce running fixture")
	}

	// Fixture initialization records an actually verified deployment response as
	// an approved Git owner. It does not qualify deployment-review UI behavior.
	server := webhookServer(t)
	fs := server.store.(*store.FSStore)
	repo := filepath.Join(server.cfg.DataDir, "stacks", stack, "repo")
	origin := filepath.Join(dir, "origin")
	gitRun := func(where string, args ...string) string {
		t.Helper()
		command := exec.CommandContext(ctx, "git", append([]string{"-C", where}, args...)...)
		b, e := command.CombinedOutput()
		if e != nil {
			t.Fatalf("synthetic Git command failed: %v %s", e, b)
		}
		return strings.TrimSpace(string(b))
	}
	os.Mkdir(origin, 0700)
	gitRun(origin, "init", "-b", "main")
	gitRun(origin, "config", "user.email", "fixture@example.invalid")
	gitRun(origin, "config", "user.name", "Synthetic fixture")
	os.WriteFile(filepath.Join(origin, "compose.pkl"), compose, 0600)
	os.WriteFile(filepath.Join(origin, "dill.pkl"), schemaBytes, 0600)
	gitRun(origin, "add", ".")
	gitRun(origin, "commit", "-m", "synthetic initial source")
	initialCommit := gitRun(origin, "rev-parse", "HEAD")
	os.MkdirAll(filepath.Dir(repo), 0700)
	gitRun(dir, "clone", "--", origin, repo)
	registered, err := fs.CreateFromGit(stack, &store.GitSource{URL: origin, Branch: "main"}, "compose.pkl")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := targetregistry.Open(filepath.Join(dir, "registry"))
	if err != nil {
		t.Fatal(err)
	}
	record := targetregistry.Record{ID: "synthetic", Revision: 1, HTTPSAddress: address, HTTPSPairing: targetregistry.PairingSecret(raw)}
	if err = registry.AddContext(ctx, record, record); err != nil {
		t.Fatal(err)
	}
	metadata, err := remoteui.DeploymentBindingMetadata(ctx, registry, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	custody := filepath.Join(dir, "custody")
	os.Mkdir(custody, 0700)
	custodyToken := filepath.Join(custody, "token")
	os.WriteFile(custodyToken, []byte(token), 0600)
	indexBytes, _ := json.Marshal(map[string]any{"version": 1, "targets": map[string]any{record.ID: map[string]string{"binding": metadata["binding"], "enrollment": metadata["enrollment"], "token_file": custodyToken}}})
	index := filepath.Join(custody, "index.json")
	os.WriteFile(index, indexBytes, 0600)
	custodian, err := remoteui.LoadDeploymentCredentials(index, server.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := remoteui.NewDeployment(server.authn, registry, fs)
	if err != nil {
		t.Fatal(err)
	}
	deployment.SetDeploymentCredentials(custodian)
	deployment.EnableGit(gitpkg.New())
	defer deployment.Close()
	server.remoteDeploy = deployment
	source := registered.RemoteGitSource()
	policy := &store.RemoteGitPolicy{Enabled: true, Host: record.ID, Target: metadata["binding"], Source: source, Phase: "succeeded", Commit: initialCommit, Seen: initialCommit, DeploymentRequest: id, DeploymentJournal: preview.JournalID}
	if err = fs.SwapRemoteGit(stack, source, nil, policy); err != nil {
		t.Fatal(err)
	}
	svc, err := webhooks.OpenExisting(webhooks.Config{Directory: server.cfg.WebhooksDir, EncryptionKey: bytes.Repeat([]byte{4}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	server.webhooks = svc
	server.webhookDispatcher = &webhookDispatcher{s: server}
	server.web.EnableWebhooks(svc, server.webhookDispatcher)
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); svc.RunTriggers(workerCtx, server.webhookDispatcher) }()
	defer func() { stopWorker(); <-workerDone }()
	operator := deployment.Protected()
	fixtureRouter := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/remote/deployment") {
			operator.ServeHTTP(w, r)
			return
		}
		server.router.ServeHTTP(w, r)
	})
	httpServer := httptest.NewUnstartedServer(fixtureRouter)
	browserFixture := os.Getenv("BRINE_REMOTE_BROWSER_FIXTURE") == "1"
	if browserFixture {
		httpServer.Listener.Close()
		listener, err := net.Listen("tcp", "0.0.0.0:18766")
		if err != nil {
			t.Fatal(err)
		}
		httpServer.Listener = listener
	}
	httpServer.Start()
	if browserFixture {
		httpServer.URL = "http://127.0.0.1:18766"
		t.Log("REMOTE_BROWSER_FIXTURE_READY")
	}
	defer httpServer.Close()
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	login, err := browser.PostForm(httpServer.URL+"/login", url.Values{"user": {"admin"}, "password": {"fixture-only-password"}})
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, login.Body)
	login.Body.Close()
	if login.StatusCode != 302 && login.StatusCode != 303 {
		t.Fatal("interactive login failed", login.StatusCode)
	}
	page, err := browser.Get(httpServer.URL + "/settings/webhooks")
	if err != nil {
		t.Fatal(err)
	}
	html, _ := io.ReadAll(page.Body)
	page.Body.Close()
	match := regexp.MustCompile(`name="csrf-token" content="([^"]+)"`).FindSubmatch(html)
	if len(match) != 2 {
		t.Fatal("missing real session CSRF", page.StatusCode)
	}
	csrf := string(match[1])
	inventory, err := browser.Get(httpServer.URL + "/api/webhooks/")
	if err != nil {
		t.Fatal(err)
	}
	var options struct {
		Options []web.WebhookOption `json:"options"`
	}
	json.NewDecoder(inventory.Body).Decode(&options)
	inventory.Body.Close()
	if len(options.Options) != 1 || options.Options[0].StackID != webhookStackID(registered) {
		t.Fatal("production Settings remote binding unavailable", options)
	}
	option := options.Options[0]
	if browserFixture {
		complete := false
		for ctx.Err() == nil {
			triggers, err := svc.Triggers(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, tr := range triggers {
				if tr.Name == "Browser remote pipeline" && !tr.Enabled {
					complete = true
				}
			}
			if complete {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !complete {
			t.Fatal("real remote Settings browser qualification did not finish")
		}
	}
	var lastTrigger webhooks.Trigger
	var lastSecret string
	var syncTrigger webhooks.Trigger
	var syncSecret string
	os.WriteFile(filepath.Join(origin, "compose.pkl"), append(append([]byte{}, compose...), []byte("\n// synthetic upstream change\n")...), 0600)
	gitRun(origin, "add", ".")
	gitRun(origin, "commit", "-m", "synthetic upstream")
	upstreamCommit := gitRun(origin, "rev-parse", "HEAD")
	for i, action := range []string{"plan", "git-sync", "stop", "start", "restart"} {
		beforeAction, _ := fs.Get(stack)
		previousHead := beforeAction.RemoteGit.DeploymentRequest
		if action == "stop" {
			running, _ := inspect()
			if !running {
				t.Fatal("stop fixture not initially running")
			}
		}
		createBody, _ := json.Marshal(webhooks.Trigger{Name: "synthetic-" + action, Enabled: true, StackID: option.StackID, TargetID: option.TargetID, Action: action})
		create := func(csrf string) *http.Response {
			r, _ := http.NewRequestWithContext(ctx, "POST", httpServer.URL+"/api/webhooks/triggers", bytes.NewReader(createBody))
			r.Header.Set("Content-Type", "application/json")
			if csrf != "" {
				r.Header.Set("X-CSRF-Token", csrf)
			}
			response, e := browser.Do(r)
			if e != nil {
				t.Fatal(e)
			}
			return response
		}
		denied := create("")
		io.Copy(io.Discard, denied.Body)
		denied.Body.Close()
		if denied.StatusCode != 403 {
			t.Fatal("trigger management bypassed CSRF", denied.StatusCode)
		}
		response := create(csrf)
		var created struct {
			Trigger webhooks.Trigger `json:"trigger"`
			Secret  string           `json:"secret"`
		}
		json.NewDecoder(response.Body).Decode(&created)
		response.Body.Close()
		if response.StatusCode != 201 || created.Secret == "" {
			t.Fatal("real session trigger creation failed", response.StatusCode)
		}
		lastTrigger, lastSecret = created.Trigger, created.Secret
		if action == "git-sync" {
			syncTrigger, syncSecret = created.Trigger, created.Secret
		}
		payload := fmt.Sprintf(`{"id":"synthetic-%d"}`, i)
		send := func(signature bool) *http.Response {
			timestamp := strconv.FormatInt(time.Now().Unix(), 10)
			r, _ := http.NewRequestWithContext(ctx, "POST", httpServer.URL+"/hooks/"+created.Trigger.ID, strings.NewReader(payload))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Brine-Timestamp", timestamp)
			if signature {
				r.Header.Set("Brine-Signature", webhooks.Sign([]byte(created.Secret), timestamp, []byte(payload)))
			}
			resp, e := http.DefaultClient.Do(r)
			if e != nil {
				t.Fatal(e)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return resp
		}
		if send(false).StatusCode != 401 {
			t.Fatal("unsigned ingress admitted")
		}
		if send(true).StatusCode != 202 {
			t.Fatal("signed ingress rejected")
		}
		deadline := time.Now().Add(20 * time.Second)
		for {
			rows, e := svc.Requests(ctx, created.Trigger.ID)
			if e != nil {
				t.Fatal(e)
			}
			if len(rows) == 1 && rows[0].Status == "completed" {
				if rows[0].Outcome != "success" {
					t.Fatal("remote request failed", rows)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("remote worker did not complete", rows)
			}
			time.Sleep(20 * time.Millisecond)
		}
		running, current := inspect()
		sourceAction := action == "plan" || action == "git-sync"
		if sourceAction {
			if !running || current != started {
				t.Fatal("source action changed runtime", action)
			}
			head := gitRun(repo, "rev-parse", "HEAD")
			if action == "plan" && head != initialCommit || action == "git-sync" && head != upstreamCommit {
				t.Fatal("source-only action did not preserve/sync expected workspace", action, head)
			}
		}
		if (action == "stop" && running) || (!sourceAction && action != "stop" && (!running || current == started)) {
			t.Fatal("signed incoming did not produce actual runtime effect", action, running)
		}
		started = current
		before, _ := fs.Get(stack)
		head := before.RemoteGit.DeploymentRequest
		if (sourceAction && head != previousHead) || (!sourceAction && (head == previousHead || head == "")) {
			t.Fatal("successful incoming did not advance reviewed target head")
		}
		if send(true).StatusCode != 202 {
			t.Fatal("duplicate not acknowledged")
		}
		after, _ := fs.Get(stack)
		if after.RemoteGit.DeploymentRequest != head {
			t.Fatal("duplicate changed deployment head")
		}
		rows, _ := svc.Requests(ctx, created.Trigger.ID)
		if len(rows) != 1 {
			t.Fatal("duplicate created new durable request")
		}
		t.Logf("signed %s completed; Settings session+CSRF, head, runtime and dedup verified", action)
	}
	// A separate authorized controller advances target lineage on the same name.
	// The old source's trigger must not inherit that replacement deployment.
	replacement, e := c.PreviewDeployment(ctx, epoch, stage, digest, stack)
	if e != nil || replacement.SelectionDigest == nil {
		t.Fatal("external replacement preview", e)
	}
	replacementID := fmt.Sprintf("%032x", 999)
	r, e := c.ApplyJournalDeployment(ctx, epoch, stage, digest, stack, *replacement.SelectionDigest, replacementID, "update", preview.JournalID)
	if e != nil || r.State != "succeeded" {
		t.Fatal("external replacement", e, r)
	}
	_, outsideStart := inspect()
	beforeReplacement, _ := fs.Get(stack)
	oldReviewedHead := beforeReplacement.RemoteGit.DeploymentRequest
	payload := []byte(`{"id":"outside-replacement"}`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req, _ := http.NewRequestWithContext(ctx, "POST", httpServer.URL+"/hooks/"+lastTrigger.ID, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Brine-Timestamp", ts)
	req.Header.Set("Brine-Signature", webhooks.Sign([]byte(lastSecret), ts, payload))
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatal("scoped replacement ingress", resp.StatusCode)
	}
	deadline := time.Now().Add(20 * time.Second)
	rejected := false
	for time.Now().Before(deadline) {
		rows, e := svc.Requests(ctx, lastTrigger.ID)
		if e != nil {
			t.Fatal(e)
		}
		for _, row := range rows {
			if row.IdempotencyKey == "outside-replacement" && row.Status == "rejected" {
				rejected = true
			}
		}
		if rejected {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !rejected {
		t.Fatal("outside replacement did not deny worker admission")
	}
	afterReplacement, _ := fs.Get(stack)
	_, afterStart := inspect()
	if afterStart != outsideStart || afterReplacement.RemoteGit.DeploymentRequest != oldReviewedHead {
		t.Fatal("outside replacement inherited scoped execution")
	}
	t.Log("same-name outside deployment cannot inherit signed trigger")
	// A fetch failing after the source mutation boundary remains unknown and
	// blocks every remote action until explicitly inspected/reconfigured.
	if e := os.Rename(origin, origin+"-offline"); e != nil {
		t.Fatal(e)
	}
	failedPayload := []byte(`{"id":"upstream-unavailable"}`)
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	failedReq, _ := http.NewRequestWithContext(ctx, "POST", httpServer.URL+"/hooks/"+syncTrigger.ID, bytes.NewReader(failedPayload))
	failedReq.Header.Set("Content-Type", "application/json")
	failedReq.Header.Set("Brine-Timestamp", stamp)
	failedReq.Header.Set("Brine-Signature", webhooks.Sign([]byte(syncSecret), stamp, failedPayload))
	failedResponse, e := http.DefaultClient.Do(failedReq)
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, failedResponse.Body)
	failedResponse.Body.Close()
	if failedResponse.StatusCode != 202 {
		t.Fatal("source unknown fixture not admitted", failedResponse.StatusCode)
	}
	deadline = time.Now().Add(20 * time.Second)
	uncertain := false
	for time.Now().Before(deadline) {
		rows, e := svc.Requests(ctx, syncTrigger.ID)
		if e != nil {
			t.Fatal(e)
		}
		for _, row := range rows {
			if row.IdempotencyKey == "upstream-unavailable" && row.Status == "unknown" {
				uncertain = true
			}
		}
		if uncertain {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	blocked, _ := fs.Get(stack)
	if !uncertain || blocked.RemoteGit.Phase != "blocked" || blocked.RemoteGit.Reason != "source-sync-unconfirmed" {
		t.Fatal("uncertain source mutation did not persist shared block")
	}
	if server.webhookDispatcher.Validate(ctx, lastTrigger) == nil {
		t.Fatal("source uncertainty did not block lifecycle")
	}
	_, afterFailedSource := inspect()
	if afterFailedSource != afterStart {
		t.Fatal("failed source sync changed runtime")
	}
	t.Log("uncertain source-only sync stays unknown and blocks remote lifecycle without deploying")
	form := url.Values{"_csrf": {csrf}, "host": {record.ID}, "stack": {stack}, "source": {source}, "target": {metadata["binding"]}, "mode": {"disable"}}
	ordinary, e := browser.PostForm(httpServer.URL+"/remote/deployment/git-config", form)
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, ordinary.Body)
	ordinary.Body.Close()
	if ordinary.StatusCode != 409 {
		t.Fatal("plain disable released uncertain source", ordinary.StatusCode)
	}
	form.Set("mode", "recover-source")
	form.Set("confirm", "inspected-source-disable")
	form.Set("source_sync", strings.Repeat("0", 32))
	stale, e := browser.PostForm(httpServer.URL+"/remote/deployment/git-config", form)
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, stale.Body)
	stale.Body.Close()
	if stale.StatusCode != 409 {
		t.Fatal("stale source incident acknowledgement accepted", stale.StatusCode)
	}
	if browserFixture {
		deadline = time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			s, e := fs.Get(stack)
			if e != nil {
				t.Fatal(e)
			}
			if !s.RemoteGit.Enabled && s.RemoteGit.Reason == "" {
				break
			}
			time.Sleep(30 * time.Millisecond)
		}
	} else {
		form.Set("mode", "recover-source")
		form.Set("confirm", "inspected-source-disable")
		form.Set("source_sync", blocked.RemoteGit.SourceSync)
		inspection, e := browser.PostForm(httpServer.URL+"/remote/deployment/git-config", form)
		if e != nil {
			t.Fatal(e)
		}
		io.Copy(io.Discard, inspection.Body)
		inspection.Body.Close()
		if inspection.StatusCode != 200 {
			t.Fatal("explicit source inspection failed", inspection.StatusCode)
		}
	}
	inspected, _ := fs.Get(stack)
	if inspected.RemoteGit.Enabled || inspected.RemoteGit.Reason != "" || inspected.RemoteGit.Phase != "disabled" || inspected.RemoteGit.SourceSync != "" {
		t.Fatal("source inspection did not disable automation")
	}
	rows, _ := svc.Requests(ctx, syncTrigger.ID)
	keptUnknown := false
	for _, r := range rows {
		if r.IdempotencyKey == "upstream-unavailable" && r.Status == "unknown" {
			keptUnknown = true
		}
	}
	_, afterInspection := inspect()
	if !keptUnknown || afterInspection != afterFailedSource {
		t.Fatal("source inspection rewrote history or executed runtime")
	}
	t.Log("authenticated source inspection disables policy without replay or outcome fabrication")
}
