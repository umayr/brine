//go:build linux

package brokerapp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/secrets"
)

func TestBrokerRuntimeLocalVault(t *testing.T) {
	if os.Getenv("BRINE_BROKER_RUNTIME_TEST") != "1" {
		t.Skip("explicit disposable runtime grant required")
	}
	t.Setenv("BRINE_BROKER_RUNTIME_PROVIDER", "vault")
	TestBrokerRuntimeLocalFile(t)
}

func runtimeVaultProvider(t *testing.T, ctx context.Context, fixture string, value []byte) *secrets.VaultConfig {
	t.Helper()
	dir := filepath.Join(fixture, "vault")
	if os.Mkdir(dir, 0700) != nil {
		t.Fatal("vault fixture mkdir")
	}
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "broker-runtime-vault"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte) {
		t.Helper()
		if os.WriteFile(filepath.Join(dir, name), b, 0600) != nil {
			t.Fatal("private fixture write failed")
		}
	}
	write("server.crt", cert)
	write("server.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}))
	write("server.hcl", []byte("disable_mlock = true\nstorage \"inmem\" {}\nlistener \"tcp\" { address = \"0.0.0.0:8200\"\n tls_cert_file = \"/fixture/server.crt\"\n tls_key_file = \"/fixture/server.key\" }\napi_addr = \"https://127.0.0.1:8200\"\n"))
	name := "broker-vault-" + filepath.Base(fixture)
	image := "hashicorp/vault@sha256:268bb80aa9c6d13d65fcfa05c0c268caca068952240a8087291a6ce0b66e3a10"
	cmd := exec.CommandContext(ctx, "docker", "run", "-d", "--pull=never", "--name", name, "--label", "brine.broker.qualifier=true", "--user", "0:0", "--cap-drop=ALL", "--security-opt", "no-new-privileges", "--memory", "192m", "--cpus", "1", "--publish", "127.0.0.1::8200", "--mount", "type=bind,src="+dir+",dst=/fixture,readonly", "--entrypoint", "vault", image, "server", "-config=/fixture/server.hcl", "-log-level=error")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated Vault startup failed: %v (%s)", err, out)
	}
	out, err := exec.CommandContext(ctx, "docker", "port", name, "8200/tcp").Output()
	if err != nil {
		t.Fatal("Vault port lookup")
	}
	endpoint := "https://" + strings.TrimSpace(string(out))
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(cert)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}, Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	ready := false
	for n := 0; n < 60; n++ {
		r, e := client.Get(endpoint + "/v1/sys/health")
		if e == nil {
			r.Body.Close()
			ready = true
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("Vault startup canceled")
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !ready {
		t.Fatal("Vault HTTPS never became ready; private container logs retained")
	}
	token := ""
	post := func(path string, input any, output any) {
		t.Helper()
		b, e := json.Marshal(input)
		if e != nil {
			t.Fatal(e)
		}
		defer clear(b)
		request, e := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+path, bytes.NewReader(b))
		if e != nil {
			t.Fatal(e)
		}
		request.Header.Set("Content-Type", "application/json")
		if token != "" {
			request.Header.Set("X-Vault-Token", token)
		}
		res, e := client.Do(request)
		if e != nil {
			t.Fatal("Vault API transport failed")
		}
		defer res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			t.Fatal("Vault API denied fixture setup", res.StatusCode)
		}
		if output != nil && json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(output) != nil {
			t.Fatal("Vault API malformed setup reply")
		}
	}
	var initialized struct {
		Keys      []string `json:"keys"`
		RootToken string   `json:"root_token"`
	}
	post("/v1/sys/init", map[string]int{"secret_shares": 1, "secret_threshold": 1}, &initialized)
	if len(initialized.Keys) != 1 || initialized.RootToken == "" {
		t.Fatal("Vault init incomplete")
	}
	token = initialized.RootToken
	post("/v1/sys/unseal", map[string]string{"key": initialized.Keys[0]}, nil)
	post("/v1/sys/mounts/secret", map[string]any{"type": "kv", "options": map[string]string{"version": "2"}}, nil)
	post("/v1/secret/data/approved/password", map[string]any{"data": map[string]string{"value": string(value)}}, nil)
	write("token", []byte(token))
	t.Setenv("SSL_CERT_FILE", filepath.Join(dir, "server.crt"))
	t.Logf("real Vault KVv2 fixture ready: container=%s image=%s TLS verified", name, image)
	return &secrets.VaultConfig{Endpoint: endpoint, Mount: "secret", PathPrefix: "approved", TokenFile: secrets.FileRef{Path: filepath.Join(dir, "token")}}
}
