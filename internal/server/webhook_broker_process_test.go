//go:build linux

package server

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/umayr/brine/internal/brokerapp"
	"github.com/umayr/brine/internal/brokerwire"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/secretbroker"
	"github.com/umayr/brine/internal/secrets"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/webhooks"
)

// This test mounts a fresh disposable engine-owned volume at its actual engine
// path. Native broker files are therefore visible to the disposable workload.
// No target/acquirer/auth/controller is replaced. Fixture routes are passive,
// isolated by a per-run access header; login is the production browser form.
// The receiver-key route configures only the ephemeral test HTTPS verifier.
func TestBrokerWebhookProductionBrowser(t *testing.T) {
	if os.Getenv("BRINE_REAL_BROKER_BROWSER") != "1" {
		t.Skip("explicit disposable broker browser grant required")
	}
	root, access := os.Getenv("BROKER_TEST_ROOT"), os.Getenv("BRINE_QUAL_ACCESS")
	if root == "" || access == "" || os.Getenv("DILL_QUAL_SOCKET") != "/var/run/docker.sock" || os.Geteuid() != 0 {
		t.Fatal("incomplete disposable runtime grant")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	engine := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: func(c context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(c, "unix", "/var/run/docker.sock")
	}}}
	requestEngine := func(method, path string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, method, "http://engine"+path, nil)
		if err != nil {
			return nil, err
		}
		res, err := engine.Do(req)
		if err != nil {
			return nil, fmt.Errorf("disposable engine unavailable")
		}
		defer res.Body.Close()
		b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		if err != nil || res.StatusCode >= 300 {
			return nil, fmt.Errorf("disposable engine %s failed: status %d", method, res.StatusCode)
		}
		return b, nil
	}
	api := func(method, path string) []byte {
		t.Helper()
		b, e := requestEngine(method, path)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	fixture, err := os.MkdirTemp(root, "broker-browser-")
	if err != nil {
		t.Fatal(err)
	}
	write := func(path string, b []byte) {
		t.Helper()
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	name := "brine-it-broker-" + hex.EncodeToString(nonce[:8])
	plain := name + "-plain"
	value := make([]byte, 32)
	if _, err = rand.Read(value); err != nil {
		t.Fatal(err)
	}
	value = []byte(hex.EncodeToString(value))
	defer clear(value)
	receiver := newBrokerReceiver(t, fixture, value)
	secretPath := filepath.Join(fixture, "input-secret")
	write(secretPath, value)
	var info struct{ ID string }
	if json.Unmarshal(api("GET", "/info"), &info) != nil || info.ID == "" {
		t.Fatal("missing exact daemon identity")
	}
	cfg := dill.LocalBrokerConfig{Binary: os.Getenv("DILL_AGENT_HTTPS_BIN"), Pkl: os.Getenv("DILL_PKL_PATH"), Socket: "/var/run/docker.sock", DaemonID: info.ID, CoordinationDir: filepath.Join(fixture, "coord"), RuntimeDir: filepath.Join(fixture, "runtime"), HistoryDir: filepath.Join(fixture, "history"), StagingRoot: filepath.Join(fixture, "stages"), CatalogRoot: filepath.Join(fixture, "catalog")}
	run := func(binary string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = append(os.Environ(), "BRINE_AUTH_USER=broker-operator", "BRINE_AUTH_PASSWORD=fixture-only-password")
		out, err := cmd.CombinedOutput()
		if bytes.Contains(out, value) {
			t.Fatal("fixture command disclosed provider bytes")
		}
		if err != nil {
			t.Fatalf("fixture command %s failed: %s", filepath.Base(binary), out)
		}
	}
	for _, p := range []string{cfg.CoordinationDir, cfg.StagingRoot, cfg.CatalogRoot} {
		if err = os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	run(cfg.Binary, "broker-init", "--contract", brokerwire.Version, "--broker-store", cfg.RuntimeDir)
	run(cfg.Binary, "broker-local", "init", "--contract", brokerwire.Version, "--coordination-state-dir", cfg.CoordinationDir, "--broker-store", cfg.RuntimeDir, "--history-dir", cfg.HistoryDir, "--staging-root", cfg.StagingRoot, "--staging-catalog", cfg.CatalogRoot, "--pkl", cfg.Pkl, "--engine-socket", cfg.Socket, "--daemon-id", cfg.DaemonID)
	data := filepath.Join(fixture, "brine")
	keys := filepath.Join(fixture, "keys")
	for _, p := range []string{data, keys, filepath.Join(fixture, "archive")} {
		if err = os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Mkdir(filepath.Join(data, "secret-state"), 0700); err != nil {
		t.Fatal(err)
	}
	fs, err := store.NewFSStore(data)
	if err != nil {
		t.Fatal(err)
	}
	source := fmt.Sprintf("amends \"dill.pkl\"\nconfig { name = %q; engine = \"docker\"; secrets { new { name = \"password\" } }; services { [\"web\"] { image = \"busybox:1.36\"; pullPolicy = \"never\"; user = \"0:0\"; command = List(\"sleep\",\"3600\"); secrets { new { name = \"password\"; target = \"/run/secrets/password\"; uid = 0; gid = 0; mode = 256 } } } } }\n", name)
	schema, err := os.ReadFile(os.Getenv("BRINE_TEST_DILL_SCHEMA"))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := fs.Create(name, source)
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(filepath.Dir(stack.PklPath), "dill.pkl"), schema)
	sibling, err := fs.Create(plain, fmt.Sprintf("amends \"dill.pkl\"\nconfig {name=%q;engine=\"docker\"; services { [\"web\"] {image=\"busybox:1.36\"} } }\n", plain))
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(filepath.Dir(sibling.PklPath), "dill.pkl"), schema)
	scope := secretbroker.Scope{RegistrationID: hex.EncodeToString(nonce), StackName: name, TargetRegistration: "local"}
	policy := brokerapp.Policy{Version: 1, Providers: secrets.Config{Version: 1, Connections: map[string]secrets.ConnectionConfig{"runtime-files": {Files: &secrets.FilesConfig{Sources: map[string]secrets.FileRef{"password": {Path: secretPath}}}}}}, Registrations: []brokerapp.Registration{{Scope: scope, Connections: []string{"runtime-files"}}}, Targets: map[string]brokerapp.TargetPolicy{"local": {Kind: "local", Local: &cfg, Grants: []string{"journal", "stage", "snapshot", "requirements", "prepare", "retire", "review", "apply", "status", "fence"}}}}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(fixture, "policy.json")
	write(policyPath, raw)
	binary := filepath.Join(t.TempDir(), "brine")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/brine")
	build.Dir = filepath.Clean(filepath.Join(wd, "../.."))
	if err = build.Run(); err != nil {
		t.Fatal("production CLI build failed")
	}
	key := func(n string) string {
		b := make([]byte, 32)
		if _, err = rand.Read(b); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(keys, n)
		write(p, b)
		clear(b)
		return p
	}
	hookKey, sourceKey := key("hooks.key"), key("source.key")
	hooks := []string{"--webhooks-enabled", "--webhooks-dir", filepath.Join(data, "webhooks"), "--webhooks-key-file", hookKey, "--webhooks-allow-private-cidr", receiver.CIDR}
	archive := []string{"--source-history-dir", filepath.Join(fixture, "archive"), "--source-history-key", sourceKey}
	run(binary, "--data-dir", data, "--init-broker-state")
	run(binary, append(append([]string{"--data-dir", data}, hooks...), "--init-webhooks")...)
	run(binary, append(append([]string{"--data-dir", data}, archive...), "--init-source-history")...)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	listener.Close()
	args := append([]string{"--data-dir", data, "--bind", "127.0.0.1", "--port", port, "--engine", "docker", "--engine-socket", "unix:///var/run/docker.sock", "--dill", cfg.Binary, "--broker-policy", policyPath}, hooks...)
	args = append(args, archive...)
	process := exec.CommandContext(ctx, binary, args...)
	process.Env = append(os.Environ(), "BRINE_AUTH_USER=broker-operator", "BRINE_AUTH_PASSWORD=fixture-only-password", "SSL_CERT_FILE="+receiver.CAFile)
	isolateFixtureProcess(process)
	if err = process.Start(); err != nil {
		t.Fatal(err)
	}
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { killFixtureGroup(process); process.Wait() }) }
	defer stop()
	base := "http://127.0.0.1:" + port
	ready := false
	for until := time.Now().Add(15 * time.Second); time.Now().Before(until); {
		res, e := http.Get(base + "/login")
		if e == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !ready {
		t.Fatal("production CLI not ready")
	}
	// Cleanup only the exact random stack's inspected container/network, never a
	// whole daemon or unrelated volume. The shell owns the fresh shared volume.
	defer func() {
		stop() // Reap the producer BEFORE the final owned-resource sweep.
		for _, resource := range []string{"containers", "networks"} {
			lookup := name
			if resource == "containers" {
				lookup = name + "_web"
			}
			req, _ := http.NewRequest("GET", "http://engine/"+resource+"/"+url.PathEscape(lookup)+map[string]string{"containers": "/json", "networks": ""}[resource], nil)
			res, e := engine.Do(req)
			if e != nil {
				t.Error("cleanup lookup unavailable")
				continue
			}
			if res.StatusCode == 404 {
				res.Body.Close()
				continue
			}
			var owned struct {
				ID     string
				Labels map[string]string
				Config struct{ Labels map[string]string }
			}
			e = json.NewDecoder(res.Body).Decode(&owned)
			res.Body.Close()
			labels := owned.Labels
			if resource == "containers" {
				labels = owned.Config.Labels
			}
			decoded, idErr := hex.DecodeString(owned.ID)
			if e != nil || idErr != nil || len(decoded) != 32 || labels["dill.stack"] != name || labels["dill.managed"] != "true" {
				t.Error("cleanup ownership unavailable; resource preserved")
				continue
			}
			req, _ = http.NewRequest("DELETE", "http://engine/"+resource+"/"+owned.ID+"?force=1", nil)
			res, e = engine.Do(req)
			if e != nil {
				t.Error("owned cleanup unavailable")
				continue
			}
			if res.StatusCode >= 300 {
				t.Error("owned cleanup refused")
			}
			res.Body.Close()
		}
	}()
	proxyURL, _ := url.Parse(base)
	proxy := httputil.NewSingleHostReverseProxy(proxyURL)
	director := proxy.Director
	proxy.Director = func(r *http.Request) { director(r); r.Header.Set("Accept-Encoding", "identity") }
	proxy.Transport = &http.Transport{DisableCompression: true}
	proxy.ModifyResponse = func(r *http.Response) error {
		if strings.Contains(r.Header.Get("Content-Type"), "text/event-stream") {
			r.Body = &brokerSecretCheckingReader{ReadCloser: r.Body, secret: value, leak: func() { t.Error("provider value leaked through production stream") }}
			return nil
		}
		b, e := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		r.Body.Close()
		if e != nil {
			return e
		}
		if bytes.Contains(b, value) {
			t.Error("provider value leaked through production HTTP")
			return fmt.Errorf("disclosure refused")
		}
		r.Body = io.NopCloser(bytes.NewReader(b))
		r.ContentLength = int64(len(b))
		return nil
	}
	finished := make(chan struct{})
	var once sync.Once
	type evidence struct{ ID, StartedAt, History string }
	snapshot := func() (evidence, error) {
		var c struct {
			ID    string
			State struct {
				Running   bool
				StartedAt string
			}
		}
		raw, e := requestEngine("GET", "/containers/"+name+"_web/json")
		if e != nil {
			return evidence{}, e
		}
		if json.Unmarshal(raw, &c) != nil || !c.State.Running || c.ID == "" || c.State.StartedAt == "" {
			return evidence{}, fmt.Errorf("unavailable workload evidence")
		}
		rawHistory, e := os.ReadFile(filepath.Join(cfg.HistoryDir, "operations.json"))
		if e != nil {
			return evidence{}, fmt.Errorf("native history missing")
		}
		var journal struct {
			Operations []struct {
				RequestID             string `json:"requestId"`
				Stack, State, Effects string
			} `json:"operations"`
		}
		if json.Unmarshal(rawHistory, &journal) != nil || len(journal.Operations) != 1 || len(journal.Operations[0].RequestID) != 32 || journal.Operations[0].Stack != name || journal.Operations[0].State != "succeeded" || journal.Operations[0].Effects != "complete" {
			return evidence{}, fmt.Errorf("native applied record unavailable")
		}
		h := sha256.New()
		e = filepath.Walk(cfg.HistoryDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if !info.Mode().IsRegular() || info.Size() > 32<<20 {
				return fmt.Errorf("unsafe history evidence")
			}
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			fmt.Fprint(h, filepath.Base(path))
			h.Write(b)
			clear(b)
			return nil
		})
		return evidence{c.ID, c.State.StartedAt, hex.EncodeToString(h.Sum(nil))}, e
	}
	var evidenceMu sync.Mutex
	beforeConfirmed := false
	var applied evidence

	verify := func() error {
		if err := receiver.wait(); err != nil {
			return err
		}
		evidenceMu.Lock()
		before, prior := beforeConfirmed, applied
		evidenceMu.Unlock()
		current, e := snapshot()
		if e != nil || !before || prior.ID == "" || current != prior {
			return fmt.Errorf("workload/history changed during read-only recovery")
		}
		var container struct {
			ID    string
			State struct{ Running bool }
		}
		raw, e := requestEngine("GET", "/containers/"+name+"_web/json")
		if e != nil || json.Unmarshal(raw, &container) != nil || container.ID == "" || !container.State.Running {
			return fmt.Errorf("workload not running")
		}
		archive, e := requestEngine("GET", "/containers/"+container.ID+"/archive?path=/run/secrets/password")
		if e != nil {
			return e
		}
		tr := tar.NewReader(bytes.NewReader(archive))
		if _, e := tr.Next(); e != nil {
			return fmt.Errorf("secret mount not readable")
		}
		b, e := io.ReadAll(tr)
		if e != nil || !bytes.Equal(b, value) {
			clear(b)
			return fmt.Errorf("runtime provider bytes mismatch")
		}
		clear(b)
		db, e := sql.Open("sqlite", "file:"+filepath.Join(data, "webhooks", "webhooks.db")+"?mode=ro")
		if e != nil {
			return e
		}
		defer db.Close()
		rows, e := db.Query("SELECT terminal FROM lifecycle_records")
		if e != nil {
			return e
		}
		defer rows.Close()
		actions := map[string]int{}
		for rows.Next() {
			var b []byte
			var event webhooks.Event
			if rows.Scan(&b) != nil || json.Unmarshal(b, &event) != nil {
				return fmt.Errorf("missing terminal broker fact")
			}
			if bytes.Contains(b, value) || bytes.Contains(b, []byte("runtime-files")) || bytes.Contains(b, []byte("generation")) {
				return fmt.Errorf("unsafe broker event metadata")
			}
			if !receiver.matches(event) {
				return fmt.Errorf("wire fact differs from durable broker event")
			}
			if event.Outcome != "success" || event.Source.Kind != "workspace" || event.Target.Kind != "local" {
				return fmt.Errorf("broker fact rewritten")
			}
			if event.Action == "plan" && event.Effects != "none" {
				return fmt.Errorf("Plan runtime effect invented")
			}
			if event.Action == "deploy" && event.Effects != "complete" {
				return fmt.Errorf("deploy receipt lost")
			}
			actions[event.Action]++
		}
		protected, e := fs.Get(name)
		if e != nil || protected.SourceHistory != nil {
			return fmt.Errorf("protected source was archived")
		}
		if rows.Err() != nil || len(actions) != 2 || actions["plan"] != 1 || actions["deploy"] != 1 {
			return fmt.Errorf("broker executions duplicated or absent")
		}
		return nil
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Brine-Qualification") != access {
			http.Error(w, "fixture access denied", 403)
			return
		}
		switch r.URL.Path {
		case "/fixture/info":
			json.NewEncoder(w).Encode(map[string]string{"stack": name, "stack_id": webhookStackID(stack), "plain": plain, "registration": scope.RegistrationID, "receiver_url": receiver.URL})
		case "/fixture/receiver-key":
			if r.Method != "POST" {
				w.WriteHeader(405)
				return
			}
			var in struct {
				Secret string `json:"secret"`
			}
			if json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&in) != nil || !receiver.setKey(in.Secret) {
				w.WriteHeader(400)
				return
			}
			w.WriteHeader(204)
		case "/fixture/before-confirm":
			for _, path := range []string{"/containers/" + name + "_web/json", "/networks/" + name} {
				req, _ := http.NewRequestWithContext(ctx, "GET", "http://engine"+path, nil)
				res, e := engine.Do(req)
				if e != nil {
					http.Error(w, "runtime evidence unavailable", 409)
					return
				}
				res.Body.Close()
				if res.StatusCode != 404 {
					t.Error("runtime resource existed before explicit confirmation")
					http.Error(w, "early deployment", 409)
					return
				}
			}
			evidenceMu.Lock()
			beforeConfirmed = true
			evidenceMu.Unlock()
			w.WriteHeader(200)
		case "/fixture/after-apply":
			current, e := snapshot()
			if e != nil {
				http.Error(w, "runtime evidence unavailable", 409)
				return
			}
			evidenceMu.Lock()
			applied = current
			evidenceMu.Unlock()
			w.WriteHeader(200)
		case "/fixture/finish":
			if r.Method != "POST" {
				w.WriteHeader(405)
				return
			}
			if e := verify(); e != nil {
				t.Error(e)
				http.Error(w, "qualification failed", 409)
			} else {
				w.WriteHeader(200)
			}
			once.Do(func() { close(finished) })
		default:
			proxy.ServeHTTP(w, r)
		}
	})
	front := &http.Server{Addr: "0.0.0.0:18767", Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	defer front.Close()
	errs := make(chan error, 1)
	go func() { errs <- front.ListenAndServe() }()
	select {
	case <-finished:
	case e := <-errs:
		t.Fatal("isolated fixture listener failed", e)
	case <-ctx.Done():
		t.Fatal("broker browser qualification timed out")
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	if e := front.Shutdown(shutdownCtx); e != nil {
		t.Error("fixture response drain unavailable")
	}
	t.Log("production CLI/auth/reference save/provider/native Plan/explicit confirmation produced exactly one plan and deploy; absent-before-confirm and runtime bytes verified privately; matching signed HTTPS notifications durably acknowledged; read-only recovery preserved container identity/start time/native history")
}

type brokerSecretCheckingReader struct {
	io.ReadCloser
	secret, tail []byte
	leak         func()
}

func (r *brokerSecretCheckingReader) Read(p []byte) (int, error) {
	n, e := r.ReadCloser.Read(p)
	window := append(append([]byte{}, r.tail...), p[:n]...)
	if bytes.Contains(window, r.secret) {
		clear(window)
		r.leak()
		return 0, fmt.Errorf("stream disclosure refused")
	}
	keep := len(r.secret) - 1
	if keep > len(window) {
		keep = len(window)
	}
	r.tail = append(r.tail[:0], window[len(window)-keep:]...)
	clear(window)
	return n, e
}
func (r *brokerSecretCheckingReader) Close() error { clear(r.tail); return r.ReadCloser.Close() }

func TestBrokerSecretStreamGuardChecksChunkBoundaries(t *testing.T) {
	secret := []byte("fixture-private-marker")
	body := io.MultiReader(strings.NewReader("prefix-"+string(secret[:7])), strings.NewReader(string(secret[7:])+"-suffix"))
	leaked := false
	r := &brokerSecretCheckingReader{ReadCloser: io.NopCloser(body), secret: secret, leak: func() { leaked = true }}
	defer r.Close()
	_, err := io.Copy(io.Discard, r)
	if err == nil || !leaked {
		t.Fatal("stream disclosure escaped chunk guard")
	}
}
