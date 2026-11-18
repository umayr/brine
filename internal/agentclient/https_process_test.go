package agentclient

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This uses the actual Dill executable and Brine client, but a synthetic engine
// served on a private Unix socket. It never contacts a real daemon or host.
func TestHTTPSPairProcess(t *testing.T) {
	bin := os.Getenv("DILL_AGENT_HTTPS_BIN")
	if bin == "" {
		t.Skip("set DILL_AGENT_HTTPS_BIN to the pairing-capable Dill build")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "bhttps-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	engine, credentials := filepath.Join(dir, "engine.sock"), filepath.Join(dir, "creds")
	l, err := net.Listen("unix", engine)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 64)
	docker := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/info":
			fmt.Fprint(w, `{"ID":"synthetic-daemon"}`)
		case r.Method == "GET" && r.URL.Path == "/containers/json":
			fmt.Fprintf(w, `[{"Id":%q,"Labels":{"dill.managed":"true","dill.stack":"demo","dill.service":"web"}}]`, id)
		case r.Method == "GET" && r.URL.Path == "/containers/"+id+"/json":
			fmt.Fprintf(w, `{"Id":%q,"Name":"/demo-web","State":{"Status":"running"},"Config":{"Image":"synthetic:test","Labels":{"dill.managed":"true","dill.stack":"demo","dill.service":"web"}}}`, id)
		default:
			t.Errorf("unexpected engine call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})}
	go docker.Serve(l)
	defer docker.Close()
	if err := exec.CommandContext(ctx, bin, "init-https", "--dir", credentials).Run(); err != nil {
		t.Fatal("initialize HTTPS", err)
	}
	reserve, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserve.Addr().String()
	const engineID = "11111111-1111-4111-8111-111111111111"
	args := []string{"--https-listen", address, "--https-dir", credentials, "--engine-socket", engine, "--engine-id", engineID, "--generation", "1", "--daemon-id", "synthetic-daemon"}
	var bundle struct {
		Version           int
		CertificateSHA256 string
		Token             string
		AgentPublicKey    string
		EngineID          string
		Generation        uint64
		EngineKind        string
		EngineSocket      string
		DaemonID          string
	}
	raw, err := exec.CommandContext(ctx, bin, append(args, "--print-https-pairing")...).Output()
	if err != nil || json.Unmarshal(raw, &bundle) != nil || bundle.Version != 1 {
		reserve.Close()
		t.Fatal("pairing export failed", err)
	}
	publicKey, err := hex.DecodeString(bundle.AgentPublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		reserve.Close()
		t.Fatal("invalid public key in bundle")
	}
	binding := Binding{publicKey, bundle.EngineID, bundle.Generation, bundle.EngineKind, bundle.EngineSocket, bundle.DaemonID}
	reserve.Close()
	process := exec.CommandContext(ctx, bin, args...)
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = process.Process.Kill(); _ = process.Wait() }()
	var client *Client
	var epoch string
	for ctx.Err() == nil {
		client, err = NewHTTPS(ctx, address, bundle.CertificateSHA256, bundle.Token, binding)
		if err != nil {
			t.Fatal("actual agent bundle rejected")
		}
		epoch, err = client.Session(ctx)
		if err == nil {
			break
		}
		client.Close()
		select {
		case <-ctx.Done():
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err != nil || epoch == "" {
		t.Fatal("authenticated agent session was not established")
	}
	defer client.Close()
	page, err := client.Inventory(ctx, epoch, nil, 20)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != id {
		t.Fatal("engine inventory did not match synthetic target", err)
	}
	for _, tt := range []struct {
		path string
		auth string
		want int
	}{
		{"/v1/inventory", "", http.StatusUnauthorized},
		{"/v1/inventory", "Bearer not-the-token", http.StatusUnauthorized},
		{"/v1/reveal", "Bearer " + bundle.Token, http.StatusNotFound},
		{"/v1/deploy", "Bearer " + bundle.Token, http.StatusNotFound},
	} {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+tt.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if tt.auth != "" {
			req.Header.Set("Authorization", tt.auth)
		}
		response, err := client.http.Do(req)
		if err != nil {
			t.Fatal("protected endpoint request failed", err)
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if readErr != nil || response.StatusCode != tt.want {
			t.Fatalf("%s: got %d, want %d", tt.path, response.StatusCode, tt.want)
		}
	}
}
