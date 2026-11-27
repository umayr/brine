//go:build linux

package web

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/brokerapp"
	"github.com/umayr/brine/internal/brokerwire"
	"github.com/umayr/brine/internal/configvars"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/secretbroker"
	"github.com/umayr/brine/internal/secrets"
	"github.com/umayr/brine/internal/store"
)

// Opt-in operator fixture: production HTTP/auth/controller/provider/target, no
// replacement acquisition or target. The extra fixture endpoints require the
// pinned browser client certificate and exist only in this test binary.
func TestBrokerRuntimeBrowserLocalFile(t *testing.T) {
	root := os.Getenv("BROKER_TEST_ROOT")
	if os.Getenv("BRINE_BROKER_RUNTIME_TEST") != "1" || root == "" {
		t.Skip("explicit disposable runtime grant required")
	}
	if os.Geteuid() != 0 {
		t.Fatal("namespace UID0 required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	fixture, err := os.MkdirTemp(root, "browser-local-")
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
	stackName := "brokerui" + hex.EncodeToString(nonce[:8])
	seed := make([]byte, 32)
	if _, err = rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	value := []byte(hex.EncodeToString(seed))
	clear(seed)
	defer clear(value)
	secretPath := filepath.Join(fixture, "input-secret")
	write(secretPath, value)
	cfg := dill.LocalBrokerConfig{Binary: filepath.Join(root, "dill"), Pkl: filepath.Join(root, "pkl"), Socket: "/var/run/docker.sock", CoordinationDir: filepath.Join(fixture, "coord"), RuntimeDir: filepath.Join(fixture, "runtime"), HistoryDir: filepath.Join(fixture, "history"), StagingRoot: filepath.Join(fixture, "stages"), CatalogRoot: filepath.Join(fixture, "catalog")}
	run := func(binary string, args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture command failed: %s / %s: %v", filepath.Base(binary), args[0], err)
		}
		return out
	}
	cfg.DaemonID = strings.TrimSpace(string(run("docker", "--host", "unix:///var/run/docker.sock", "info", "--format", "{{.ID}}")))
	for _, p := range []string{cfg.CoordinationDir, cfg.StagingRoot, cfg.CatalogRoot} {
		if err = os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	run(cfg.Binary, "broker-init", "--contract", brokerwire.Version, "--broker-store", cfg.RuntimeDir)
	run(cfg.Binary, "broker-local", "init", "--contract", brokerwire.Version, "--coordination-state-dir", cfg.CoordinationDir, "--broker-store", cfg.RuntimeDir, "--history-dir", cfg.HistoryDir, "--staging-root", cfg.StagingRoot, "--staging-catalog", cfg.CatalogRoot, "--pkl", cfg.Pkl, "--engine-socket", cfg.Socket, "--daemon-id", cfg.DaemonID)
	data := filepath.Join(fixture, "brine")
	if err = os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	if err = brokerapp.Initialize(data); err != nil {
		t.Fatal(err)
	}
	st, err := store.NewFSStore(data)
	if err != nil {
		t.Fatal(err)
	}
	source := "amends \"dill.pkl\"\nconfig { name = \"" + stackName + "\"\nengine = \"docker\"\nsecrets { new { name = \"password\" } }\nservices { [\"web\"] { image = \"nginx:alpine\"; pullPolicy = \"never\"; user = \"0:0\"; command = List(\"sleep\",\"3600\"); secrets { new { name = \"password\"; target = \"/run/secrets/password\"; uid = 0; gid = 0; mode = 256 } } } } }\n"
	stack, err := st.Create(stackName, source)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile(filepath.Join(root, "dill.pkl"))
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(filepath.Dir(stack.PklPath), "dill.pkl"), schema)
	scope := secretbroker.Scope{RegistrationID: hex.EncodeToString(nonce), StackName: stackName, TargetRegistration: "local"}
	policy := brokerapp.Policy{Version: 1, Providers: secrets.Config{Version: 1, Connections: map[string]secrets.ConnectionConfig{"runtime-files": {Files: &secrets.FilesConfig{Sources: map[string]secrets.FileRef{"password": {Path: secretPath}}}}}}, Registrations: []brokerapp.Registration{{Scope: scope, Connections: []string{"runtime-files"}}}, Targets: map[string]brokerapp.TargetPolicy{"local": {Kind: "local", Local: &cfg, Grants: []string{"journal", "stage", "snapshot", "requirements", "prepare", "retire", "review", "apply", "status", "fence"}}}}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(fixture, "policy.json")
	write(policyPath, raw)
	svc, err := brokerapp.Open(data, policyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	// The binding is deliberately NOT seeded: the browser must save the actual
	// Variables reference before preparing the real target.
	passwordBytes := make([]byte, 32)
	if _, err = rand.Read(passwordBytes); err != nil {
		t.Fatal(err)
	}
	password := hex.EncodeToString(passwordBytes)
	clear(passwordBytes)
	authn, err := auth.New(auth.Config{User: "runtime-operator", Password: password})
	if err != nil {
		t.Fatal(err)
	}
	runner := dill.New(cfg.Binary)
	runner.PklBinary = cfg.Pkl
	variables, err := configvars.New(data, st)
	if err != nil {
		t.Fatal(err)
	}
	runner.Variables = variables
	h := New(agent.NewLocalAgent(nil, runner), st, nil, &git.Runner{Binary: "/never-executed-git"}, "docker", false, authn, time.Minute)
	h.EnableBroker(svc)
	router := chi.NewRouter()
	router.Use(authn.Middleware)
	Mount(router, h, httpx.Timeouts{})
	finished := make(chan struct{})
	var once sync.Once
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded := httptest.NewRecorder()
		switch r.URL.Path {
		case "/fixture/login":
			form := url.Values{"user": {"runtime-operator"}, "password": {password}}
			login := r.Clone(r.Context())
			login.Method = "POST"
			login.Body = io.NopCloser(strings.NewReader(form.Encode()))
			login.Header = make(http.Header)
			login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			login.ContentLength = int64(len(form.Encode()))
			authn.HandleLogin(recorded, login)
		case "/fixture/info":
			json.NewEncoder(recorded).Encode(map[string]string{"stack": stackName})
		case "/fixture/finish":
			if r.Method != "POST" {
				recorded.WriteHeader(405)
				break
			}
			records, e := svc.State.Deployments(scope)
			if e != nil || len(records) != 1 || len(records[0].Events) == 0 || records[0].Events[len(records[0].Events)-1].State != "succeeded" {
				t.Error("browser did not produce exactly one authoritative successful deployment")
				recorded.WriteHeader(409)
			} else {
				out, e := exec.CommandContext(ctx, "docker", "--host", "unix:///var/run/docker.sock", "exec", stackName+"_web", "cat", "/run/secrets/password").Output()
				if e != nil || !bytes.Equal(out, value) {
					t.Error("browser deployment did not deliver exact provider bytes")
					recorded.WriteHeader(409)
				} else {
					recorded.WriteHeader(200)
				}
				clear(out)
			}
			once.Do(func() { close(finished) })
		default:
			router.ServeHTTP(recorded, r)
		}
		if bytes.Contains(recorded.Body.Bytes(), value) {
			t.Error("provider bytes leaked through browser HTTP")
			http.Error(w, "fixture disclosure refused", 500)
			return
		}
		for key, values := range recorded.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(recorded.Code)
		w.Write(recorded.Body.Bytes())
	})
	clientPEM, err := os.ReadFile(filepath.Join(root, "browser-client.crt"))
	if err != nil {
		t.Fatal(err)
	}
	clients := x509.NewCertPool()
	if !clients.AppendCertsFromPEM(clientPEM) {
		t.Fatal("browser client CA invalid")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Brine disposable runtime browser"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		if network, ok := address.(*net.IPNet); ok {
			template.IPAddresses = append(template.IPAddresses, network.IP)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	tlsListener := tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clients})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	defer func() {
		stop, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		server.Shutdown(stop)
	}()
	go server.Serve(tlsListener)
	fmt.Printf("RUNTIME_BROWSER_FIXTURE=%s\nRUNTIME_BROWSER_PORT=%d\nRUNTIME_BROWSER_CERT=%s\n", fixture, listener.Addr().(*net.TCPAddr).Port, base64.StdEncoding.EncodeToString(leaf))
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("browser fixture timed out; retain evidence")
	}
	if t.Failed() {
		return
	}
	t.Log("PASS production browser -> real file acquisition -> reviewed native local Docker apply -> exact container bytes and authoritative shared History; no HTTP value disclosure")
}
