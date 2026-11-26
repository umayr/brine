package agentclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Explicit opt-in: this mutates ONLY the disposable daemon supplied by the caller.
func TestHTTPSRealManagedLifecycle(t *testing.T) {
	if os.Getenv("DILL_AGENT_REAL_LIFECYCLE") != "1" {
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
	for _, grant := range []string{"stage", "manifest", "preview", "journal-apply", "start-preview", "start", "stop-preview", "stop", "restart-preview", "restart", "status", "fence"} {
		args = append(args, "--https-deploy-grant", grant)
	}
	raw, e := command(append(args, "--print-https-pairing")...).Output()
	if e != nil {
		reserve.Close()
		t.Fatal("pair export", e)
	}
	pair, e := ParsePairing(raw)
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
	var c *Client
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
	id := fmt.Sprintf("%032x", 1)
	result, e := c.ApplyJournalDeployment(ctx, epoch, stage, digest, stack, *preview.SelectionDigest, id, "create", preview.JournalID)
	if e != nil || result.State != "succeeded" {
		t.Fatal("create", e, result, logs.String())
	}
	var containers []struct {
		ID     string `json:"Id"`
		Labels map[string]string
	}
	if json.Unmarshal(engineCall("GET", "/containers/json?all=1"), &containers) != nil {
		t.Fatal("fixture inventory")
	}
	createdIDs := []string{}
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
	// Pin cleanup immediately after successful creation. A later object with
	// the same stack/network name is not this test's cleanup authority.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, path := range append([]string{"/containers/" + createdIDs[0] + "?force=1"}, "/networks/"+network.ID) {
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
	_, started := inspect()
	// Monitor pairing alone must not acquire execution authority on the wire.
	monitor, _, err := pair.Open(ctx, ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", monitor.baseURL+"/v1/deploy/stop-preview", bytes.NewBufferString(fmt.Sprintf(`{"stack":%q}`, stack)))
	req.Header.Set("Authorization", "Bearer "+pair.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(journalHeader, preview.JournalID)
	denied, err := monitor.http.Do(req)
	if err != nil {
		monitor.Close()
		t.Fatal(err)
	}
	io.Copy(io.Discard, denied.Body)
	denied.Body.Close()
	monitor.Close()
	if denied.StatusCode != http.StatusForbidden {
		t.Fatal("monitor gained lifecycle authority")
	}
	for i, action := range []string{"stop", "stop", "start", "restart"} {
		p, e := c.PreviewLifecycle(ctx, epoch, stack, action, preview.JournalID)
		if e != nil || p.SelectionDigest == nil {
			t.Fatal("lifecycle preview", action, e)
		}
		request := fmt.Sprintf("%032x", i+2)
		r, e := c.ApplyLifecycle(ctx, epoch, stack, action, *p.SelectionDigest, request, p.PriorRequestID, preview.JournalID)
		if e != nil || r.State != "succeeded" || r.Action != action {
			t.Fatal("lifecycle apply", action, e, r)
		}
		f, e := c.FenceDeployment(ctx, epoch, request, stack, preview.JournalID)
		if e != nil || f != r {
			t.Fatal("fence lost existing result", e, f, r)
		}
		running, currentStart := inspect()
		if (action == "stop" && running) || (action != "stop" && (!running || currentStart == started)) {
			t.Fatal("receipt did not match actual lifecycle state", action, running)
		}
		started = currentStart
		// Require an observed journal-bound HTTP conflict, not any transport error.
		// This denial is last since it can retire the signed transport proof.
		if i == 3 {
			payload, _ := json.Marshal(map[string]string{"stack": stack, "selectionDigest": *p.SelectionDigest, "requestId": request, "priorRequestId": p.PriorRequestID, "confirm": action})
			req, _ := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/deploy/"+action, bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(journalHeader, preview.JournalID)
			resp, err := c.do(req)
			if err != nil {
				t.Fatal("replay did not reach agent", err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusConflict || resp.Header.Get(journalHeader) != preview.JournalID {
				t.Fatal("missing journal-bound replay denial", resp.StatusCode)
			}
			running, after := inspect()
			if !running || after != started {
				t.Fatal("duplicate changed container start identity")
			}
		}
		t.Logf("%s state=%s effects=%s", action, r.State, r.Effects)
	}
}
