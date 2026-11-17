package agentclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// boundedOutput retains at most 64 KiB; truncation fails the test so a
// sentinel hidden beyond the bound cannot be mistaken for a clean output.
type boundedOutput struct {
	buf      bytes.Buffer
	overflow bool
}

func (w *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 64*1024 - w.buf.Len()
	if len(p) > remaining {
		w.overflow = true
		p = p[:remaining]
	}
	_, _ = w.buf.Write(p)
	return n, nil
}

// TestPairProcess uses only a synthetic Unix-socket engine, never a Docker daemon.
func TestPairProcess(t *testing.T) {
	base, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	bin := os.Getenv("DILL_AGENT_BIN")
	if bin == "" {
		t.Skip("set DILL_AGENT_BIN to an independently built dill-agent")
	}
	bin, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	// Keep socket paths short enough for Unix-domain sockets on macOS.
	dir, err := os.MkdirTemp("", "bp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	engine := filepath.Join(dir, "engine.sock")
	agent := filepath.Join(dir, "agent.sock")
	l, err := net.Listen("unix", engine)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 64)
	unmanaged := strings.Repeat("b", 64)
	const secret = "synthetic-secret-not-for-output"
	labels := map[string]string{"dill.managed": "true", "dill.stack": "demo", "dill.service": "web"}
	var mu sync.Mutex
	daemon := "fake-daemon"
	mismatch := false
	var unexpected []string
	disconnected := make(chan struct{}, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/containers/"+id+"/logs" {
			payload := []byte("synthetic log line\n")
			var header [8]byte
			header[0] = 1
			binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
			_, _ = w.Write(header[:])
			_, _ = w.Write(payload)
			w.(http.Flusher).Flush()
			if r.URL.Query().Get("follow") == "true" {
				<-r.Context().Done()
				disconnected <- struct{}{}
			}
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.Method != "GET" {
			unexpected = append(unexpected, r.Method+" "+r.URL.Path)
			http.Error(w, "forbidden", 403)
			return
		}
		switch {
		case r.URL.Path == "/info":
			_ = json.NewEncoder(w).Encode(map[string]any{"ID": daemon})
		case r.URL.Path == "/containers/json":
			_ = json.NewEncoder(w).Encode([]any{map[string]any{"Id": id, "Labels": labels}, map[string]any{"Id": unmanaged, "Labels": map[string]string{}}})
		case r.URL.Path == "/containers/"+id+"/json" || r.URL.Path == "/containers/"+unmanaged+"/json":
			target := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/json")
			lab := labels
			if target == unmanaged {
				lab = map[string]string{}
			}
			actual := target
			if mismatch {
				actual = unmanaged
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": actual, "Name": "/web", "Image": "sha256:synthetic", "Config": map[string]any{"Image": "example", "Labels": lab, "Env": []string{"TOKEN=" + secret, "MODE=test"}}, "State": map[string]any{"Status": "running"}, "HostConfig": map[string]any{"LogConfig": map[string]any{"Type": "json-file"}}})
		default:
			unexpected = append(unexpected, r.Method+" "+r.URL.Path)
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(l) }()
	t.Cleanup(func() { _ = server.Close() })
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	key := ed25519.NewKeyFromSeed(seed)
	revealSeed := make([]byte, 32)
	for i := range revealSeed {
		revealSeed[i] = byte(i + 33)
	}
	signer := ed25519.NewKeyFromSeed(revealSeed)
	keyfile := filepath.Join(dir, "key")
	if err = os.WriteFile(keyfile, []byte(hex.EncodeToString(seed)), 0600); err != nil {
		t.Fatal(err)
	}
	const engineID = "12345678-1234-1234-1234-123456789abc"
	cmd := exec.Command(bin, "--socket", agent, "--engine-socket", engine, "--engine-id", engineID, "--daemon-id", "fake-daemon", "--generation", "1", "--key-file", keyfile, "--reveal-public-key", hex.EncodeToString(signer.Public().(ed25519.PublicKey)))
	// Deliberately do not inherit Docker environment variables or print process output.
	var processOutput boundedOutput
	cmd.Stdout = &processOutput
	cmd.Stderr = &processOutput
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if processOutput.overflow {
			t.Error("agent process output exceeded capture limit")
		}
		if bytes.Contains(processOutput.buf.Bytes(), []byte(secret)) {
			t.Error("agent process output contained synthetic environment value")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err = os.Stat(agent); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent socket did not appear")
		}
		time.Sleep(10 * time.Millisecond)
	}
	binding := Binding{AgentPublicKey: key.Public().(ed25519.PublicKey), EngineID: engineID, Generation: 1, EngineKind: "docker", EngineSocket: "unix://" + engine, DaemonID: "fake-daemon"}
	newClient := func(b Binding) (*Client, string) {
		t.Helper()
		c, e := NewWithRevealKey(base, agent, b, signer.Public().(ed25519.PublicKey))
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = c.Close() })
		epoch, e := c.Session(base)
		if e != nil {
			t.Fatal("session proof rejected:", e)
		}
		return c, epoch
	}
	c, epoch := newClient(binding)
	p, err := c.Inventory(base, epoch, nil, 100)
	if err != nil {
		t.Fatal("inventory:", err)
	}
	if len(p.Items) != 1 || p.Items[0].ID != id {
		t.Fatal("inventory ownership filter mismatch")
	}
	// Capture the raw agent response before any reveal, not just the normalized client value.
	response, _, err := c.request(base, nil, "/v1/inspect", epoch, struct {
		ID string `json:"containerId"`
	}{id})
	if err != nil {
		t.Fatal("raw inspect request failed:", err)
	}
	rawInspect, err := io.ReadAll(io.LimitReader(response.Body, 512*1024+1))
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || len(rawInspect) > 512*1024 {
		t.Fatal("raw inspect response failed")
	}
	if bytes.Contains(rawInspect, []byte(secret)) {
		t.Fatal("raw inspect exposed synthetic environment value")
	}
	inspect, err := c.Inspect(base, epoch, id)
	if err != nil {
		t.Fatal("inspect:", err)
	}
	raw, _ := json.Marshal(inspect)
	if strings.Contains(string(raw), secret) || strings.Join(inspect.Inspect.EnvKeys, ",") != "MODE,TOKEN" {
		t.Fatal("inspect environment filtering failed")
	}
	now := uint64(time.Now().Unix())
	enc := base64.RawURLEncoding.EncodeToString
	g := Grant{AgentID: shaAgent(binding.AgentPublicKey), EngineID: engineID, Generation: 1, Epoch: epoch, ContainerID: id, SessionID: enc(make([]byte, 16)), Nonce: enc(make([]byte, 32)), IssuedAt: now, ExpiresAt: now + 30, Scope: "env.values", Interactive: true}
	msg, err := GrantBytes(g)
	if err != nil {
		t.Fatal(err)
	}
	g.Signature = enc(ed25519.Sign(signer, msg))
	result, err := c.Reveal(base, epoch, id, g)
	if err != nil {
		t.Fatal("reveal:", err)
	}
	found := false
	for _, v := range result.Env {
		if v.Key == "TOKEN" && v.Value == secret {
			found = true
		}
	}
	if !found {
		t.Fatal("reveal missing expected synthetic value")
	}
	if _, err = c.Reveal(base, epoch, id, g); !errors.Is(err, ErrProtocol) {
		t.Fatal("client replay was not rejected locally")
	}
	cr, er := newClient(binding)
	if er != epoch {
		t.Fatal("unexpected epoch change")
	}
	if _, err = cr.Reveal(base, er, id, g); err == nil {
		t.Fatal("agent replay accepted")
	} else if re, ok := err.(*RemoteError); !ok || (re.Code != "REPLAY" && re.Code != "UNAUTHORIZED") {
		t.Fatal("agent did not report replay denial:", err)
	}
	cl, el := newClient(binding)
	var got string
	err = cl.Logs(base, el, id, LogOptions{Tail: 1}, func(_ context.Context, event LogEvent) error { got += string(event.Bytes); return nil })
	if err != nil || got != "synthetic log line\n" {
		t.Fatal("tail failed:", err)
	}
	cf, ef := newClient(binding)
	ctx, cancel := context.WithCancel(base)
	err = cf.Logs(ctx, ef, id, LogOptions{Tail: 1, Follow: true}, func(_ context.Context, event LogEvent) error {
		if len(event.Bytes) > 0 {
			cancel()
		}
		return nil
	})
	if err == nil {
		t.Fatal("cancelled stream returned success")
	}
	select {
	case <-disconnected:
	case <-time.After(3 * time.Second):
		t.Fatal("engine stream not disconnected")
	}
	forward, loseForward := context.WithCancel(base)
	cc, err := New(forward, agent, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	ec, err := cc.Session(base)
	if err != nil {
		t.Fatal("forward session:", err)
	}
	loseForward()
	if _, err = cc.Inventory(base, ec, nil, 1); err == nil {
		t.Fatal("lost forward remained usable")
	}
	// Narrow epoch-bearing probes, not authentication or exhaustive mutation proof.
	// The agent socket controls caller access; unsupported routes are rejected
	// before the agent checks binding or epoch.
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", agent)
	}}
	hc := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	defer transport.CloseIdleConnections()
	for _, route := range []string{"/v1/start", "/v1/stop", "/v1/delete", "/v1/containers/" + id + "/stop"} {
		req, e := http.NewRequestWithContext(base, http.MethodPost, "http://unix"+route, strings.NewReader("{}"))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-Id", strings.Repeat("1", 32))
		req.Header.Set("X-Brine-Binding", "1")
		req.Header.Set("X-Agent-Epoch", epoch)
		resp, e := hc.Do(req)
		if e != nil {
			t.Fatal("mutation probe failed:", e)
		}
		body, e := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		var denial struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if e != nil || json.Unmarshal(body, &denial) != nil || resp.StatusCode != 400 || denial.Error.Code != "INVALID_REQUEST" {
			t.Fatal("mutation route was not rejected as an invalid request")
		}
	}
	c2, e2 := newClient(binding)
	if _, err = c2.Inspect(base, e2, unmanaged); err == nil {
		t.Fatal("unmanaged inspection accepted")
	} else if re, ok := err.(*RemoteError); !ok || re.Code != "NOT_FOUND" {
		t.Fatal("unmanaged inspection did not receive agent denial")
	}
	mu.Lock()
	mismatch = true
	mu.Unlock()
	c3, e3 := newClient(binding)
	if _, err = c3.Inspect(base, e3, id); err == nil {
		t.Fatal("identity mismatch accepted")
	} else if re, ok := err.(*RemoteError); !ok || re.Code != "NOT_FOUND" {
		t.Fatal("identity mismatch did not receive agent denial")
	}
	mu.Lock()
	mismatch = false
	mu.Unlock()
	// Obtain the real signed proof independently to distinguish proof validation
	// failures from server-side session errors hidden by Client.Session.
	nonce := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	sessionRequest := func() (int, []byte) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"nonce": nonce})
		req, e := http.NewRequestWithContext(base, http.MethodPost, "http://unix/v1/session", bytes.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-Id", strings.Repeat("2", 32))
		req.Header.Set("X-Brine-Binding", "1")
		resp, e := hc.Do(req)
		if e != nil {
			t.Fatal("raw session failed:", e)
		}
		raw, e := io.ReadAll(io.LimitReader(resp.Body, 4097))
		_ = resp.Body.Close()
		if e != nil || len(raw) > 4096 {
			t.Fatal("raw session response failed")
		}
		return resp.StatusCode, raw
	}
	status, sessionRaw := sessionRequest()
	var signed struct {
		Data Proof `json:"data"`
	}
	if status != 200 || json.Unmarshal(sessionRaw, &signed) != nil || VerifyProof(binding, signed.Data, nonce) != nil {
		t.Fatal("valid raw session proof failed")
	}
	wrongKey := binding
	wrongKey.AgentPublicKey = signer.Public().(ed25519.PublicKey)
	ck, err := New(base, agent, wrongKey)
	if err != nil {
		t.Fatal(err)
	}
	defer ck.Close()
	if !errors.Is(VerifyProof(wrongKey, signed.Data, nonce), ErrProtocol) {
		t.Fatal("wrong agent key proof accepted")
	}
	if _, err = ck.Session(base); !errors.Is(err, ErrProtocol) {
		t.Fatal("incorrect agent key did not return protocol error")
	}
	wrong := binding
	wrong.DaemonID = "wrong"
	cw, err := New(base, agent, wrong)
	if err != nil {
		t.Fatal(err)
	}
	defer cw.Close()
	if !errors.Is(VerifyProof(wrong, signed.Data, nonce), ErrProtocol) {
		t.Fatal("wrong daemon binding proof accepted")
	}
	if _, err = cw.Session(base); !errors.Is(err, ErrProtocol) {
		t.Fatal("incorrect binding did not return protocol error")
	}
	mu.Lock()
	daemon = "drift"
	mu.Unlock()
	status, sessionRaw = sessionRequest()
	var driftDenial struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if status != 409 || json.Unmarshal(sessionRaw, &driftDenial) != nil || driftDenial.Error.Code != "BINDING_CHANGED" {
		t.Fatal("engine drift did not receive agent BINDING_CHANGED")
	}
	cd, err := New(base, agent, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer cd.Close()
	if _, err = cd.Session(base); !errors.Is(err, ErrProtocol) {
		t.Fatal("engine drift did not return protocol error")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(unexpected) > 0 {
		t.Fatal(fmt.Sprint("unexpected engine endpoints: ", unexpected))
	}
}
