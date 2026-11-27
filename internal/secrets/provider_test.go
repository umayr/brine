package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const synthetic = "synthetic-only-do-not-print"

func writeFixture(t *testing.T, dir, name, content string) FileRef {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return FileRef{Path: p}
}

func requireLinuxFiles(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("provider authentication requires secure Linux file reader")
	}
}

func vaultFixture(t *testing.T, endpoint string, token FileRef) Config {
	t.Helper()
	return Config{Version: SchemaVersion, Connections: map[string]ConnectionConfig{
		"vault": {Vault: &VaultConfig{Endpoint: endpoint, Mount: "kv", PathPrefix: "approved/team", TokenFile: token}},
	}}
}

func vaultBinding() Selector {
	return Selector{Version: SchemaVersion, Connection: "vault", Vault: &VaultSelector{Path: "app/config", Key: "password", Revision: 3}}
}

func tlsRegistry(t *testing.T, srv *httptest.Server, cfg Config) *Registry {
	t.Helper()
	r, err := newRegistry(cfg, srv.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func assertAcquisition(t *testing.T, r *Registry, ctx context.Context, sel Selector, want string, wantErr error) {
	t.Helper()
	got, err := r.Acquire(ctx, sel)
	if err != wantErr {
		t.Fatalf("acquisition error: got %v, want %v", err, wantErr)
	}
	if err != nil {
		if got.Bytes() != nil {
			t.Fatal("failed acquisition returned plaintext")
		}
		if strings.Contains(err.Error(), synthetic) {
			t.Fatal("error exposed provider data")
		}
		return
	}
	if string(got.Bytes()) != want {
		t.Fatal("acquired value differs from fixture")
	}
}

func TestVaultKVv2(t *testing.T) {
	requireLinuxFiles(t)
	token := writeFixture(t, t.TempDir(), "vault-token", "fixture-vault-token\n")
	const success = `{"data":{"data":{"password":"` + synthetic + `","other":"not-selected"},"metadata":{"version":3}}}`
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
		err    error
	}{
		{"success", 200, success, synthetic, nil},
		{"denied", 403, synthetic, "", ErrDenied},
		{"missing document", 404, synthetic, "", ErrNotFound},
		{"missing field", 200, `{"data":{"data":{"Password":"wrong-case"},"metadata":{"version":3}}}`, "", ErrNotFound},
		{"version mismatch", 200, strings.Replace(success, `"version":3`, `"version":4`, 1), "", ErrVersionMismatch},
		{"missing metadata", 200, `{"data":{"data":{"password":"hidden"}}}`, "", ErrInvalidResponse},
		{"destroyed", 200, strings.Replace(success, `"version":3`, `"version":3,"destroyed":true`, 1), "", ErrNotFound},
		{"deleted", 200, strings.Replace(success, `"version":3`, `"version":3,"deletion_time":"2026-01-01T00:00:00Z"`, 1), "", ErrNotFound},
		{"non-string field", 200, strings.Replace(success, `"`+synthetic+`"`, `null`, 1), "", ErrInvalidResponse},
		{"empty field", 200, strings.Replace(success, synthetic, "", 1), "", nil},
		{"oversize field", 200, strings.Replace(success, synthetic, strings.Repeat("x", MaxValueBytes+1), 1), "", ErrTooLarge},
		{"oversize body", 200, strings.Repeat("x", maxResponseBytes+1), "", ErrTooLarge},
		{"malformed body", 200, success + synthetic, "", ErrInvalidResponse},
		{"upstream failure", 502, synthetic, "", ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/kv/data/approved/team/app/config" || r.URL.RawQuery != "version=3" ||
					r.Header.Get("X-Vault-Token") != "fixture-vault-token" || r.TLS == nil {
					t.Error("unexpected Vault request")
				}
				w.WriteHeader(tc.status)
				if tc.name == "oversize body" {
					// Bound streamed bodies even when Content-Length is absent.
					w.(http.Flusher).Flush()
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			r := tlsRegistry(t, srv, vaultFixture(t, srv.URL, token))
			if caps, err := r.Capabilities("vault"); err != nil || caps != (Capabilities{Kind: KindVault, VersionPin: true}) {
				t.Fatal("unexpected Vault capabilities")
			}
			assertAcquisition(t, r, context.Background(), vaultBinding(), tc.want, tc.err)
		})
	}
}

func TestVaultReadsFreshCredentialsAndRejectsMultiline(t *testing.T) {
	requireLinuxFiles(t)
	dir := t.TempDir()
	token := writeFixture(t, dir, "token", "first\r\n")
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "first"
		if calls.Add(1) == 2 {
			want = "second"
		}
		if r.Header.Get("X-Vault-Token") != want || r.URL.RawQuery != "" {
			t.Error("expected a fresh credential and an unpinned read")
		}
		_, _ = io.WriteString(w, `{"data":{"data":{"password":"fresh"},"metadata":{"version":7}}}`)
	}))
	defer srv.Close()
	r := tlsRegistry(t, srv, vaultFixture(t, srv.URL, token))
	s := vaultBinding()
	s.Vault.Revision = 0
	assertAcquisition(t, r, context.Background(), s, "fresh", nil)
	writeFixture(t, dir, "token", "second")
	assertAcquisition(t, r, context.Background(), s, "fresh", nil)
	writeFixture(t, dir, "token", "second\n\r\n")
	assertAcquisition(t, r, context.Background(), s, "", ErrDenied)
	writeFixture(t, dir, "token", strings.Repeat("x", maxCredentialBytes+1))
	assertAcquisition(t, r, context.Background(), s, "", ErrTooLarge)
	if calls.Load() != 2 {
		t.Fatal("refused credentials were sent upstream")
	}
}

func TestInfisicalUniversalAuth(t *testing.T) {
	requireLinuxFiles(t)
	dir := t.TempDir()
	id := writeFixture(t, dir, "id", "fixture-id")
	secret := writeFixture(t, dir, "client-secret", "fixture-client-secret\n")
	const success = `{"secret":{"secretKey":"PASSWORD","secretValue":"` + synthetic + `","version":5}}`
	for _, tc := range []struct {
		name        string
		loginStatus int
		loginBody   string
		readStatus  int
		readBody    string
		err         error
	}{
		{"success", 200, `{"accessToken":"fixture-access-token"}`, 200, success, nil},
		{"auth denied", 401, synthetic, 0, "", ErrDenied},
		{"unsafe token", 200, `{"accessToken":"injected\r\nheader"}`, 0, "", ErrInvalidResponse},
		{"read denied", 200, `{"accessToken":"fixture-access-token"}`, 403, synthetic, ErrDenied},
		{"missing", 200, `{"accessToken":"fixture-access-token"}`, 404, synthetic, ErrNotFound},
		{"version mismatch", 200, `{"accessToken":"fixture-access-token"}`, 200, strings.Replace(success, `"version":5`, `"version":6`, 1), ErrVersionMismatch},
		{"unexpected key", 200, `{"accessToken":"fixture-access-token"}`, 200, strings.Replace(success, "PASSWORD", "OTHER", 1), ErrInvalidResponse},
		{"hidden value", 200, `{"accessToken":"fixture-access-token"}`, 200, strings.Replace(success, `"version":5`, `"version":5,"secretValueHidden":true`, 1), ErrInvalidResponse},
		{"oversize login", 200, strings.Repeat("x", maxResponseBytes+1), 0, "", ErrTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var loginCalls, readCalls atomic.Int32
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/universal-auth/login":
					loginCalls.Add(1)
					var creds map[string]string
					if r.Method != http.MethodPost || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" ||
						r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&creds) != nil ||
						len(creds) != 2 || creds["clientId"] != "fixture-id" || creds["clientSecret"] != "fixture-client-secret" {
						t.Error("unexpected Universal Auth request")
					}
					w.WriteHeader(tc.loginStatus)
					_, _ = io.WriteString(w, tc.loginBody)
				case "/api/v4/secrets/PASSWORD":
					readCalls.Add(1)
					if tc.readStatus == 0 {
						t.Error("read followed a failed login")
						w.WriteHeader(500)
						return
					}
					if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fixture-access-token" || loginCalls.Load() <= 0 {
						t.Error("unexpected Infisical read request")
					}
					q := r.URL.Query()
					if len(q) != 7 || q.Get("projectId") != "fixture-project" || q.Get("environment") != "prod" ||
						q.Get("secretPath") != "/approved/service" || q.Get("version") != "5" || q.Get("type") != "shared" ||
						q.Get("includeImports") != "false" || q.Get("expandSecretReferences") != "false" {
						t.Error("unexpected Infisical scope or options")
					}
					w.WriteHeader(tc.readStatus)
					_, _ = io.WriteString(w, tc.readBody)
				default:
					t.Error("unexpected Infisical route")
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			cfg := Config{Version: SchemaVersion, Connections: map[string]ConnectionConfig{
				"infisical": {Infisical: &InfisicalConfig{Endpoint: srv.URL, ProjectID: "fixture-project", Environment: "prod", Folder: "/approved", ClientIDFile: id, ClientSecretFile: secret}},
			}}
			r := tlsRegistry(t, srv, cfg)
			if caps, err := r.Capabilities("infisical"); err != nil || caps != (Capabilities{Kind: KindInfisical, VersionPin: true}) {
				t.Fatal("unexpected Infisical capabilities")
			}
			s := Selector{Version: SchemaVersion, Connection: "infisical", Infisical: &InfisicalSelector{Folder: "/approved/service", Key: "PASSWORD", Revision: 5}}
			unsafe := s
			unsafe.Infisical = &InfisicalSelector{Folder: "../outside", Key: "PASSWORD"}
			assertAcquisition(t, r, context.Background(), unsafe, "", ErrInvalidSelector)
			assertAcquisition(t, r, context.Background(), s, synthetic, tc.err)
			wantLogin, wantRead := int32(1), int32(0)
			if tc.readStatus != 0 {
				wantRead++
			}
			if tc.err == nil {
				assertAcquisition(t, r, context.Background(), s, synthetic, nil)
				wantLogin++
				wantRead++
			}
			if loginCalls.Load() != wantLogin || readCalls.Load() != wantRead {
				t.Fatal("unexpected login/read count")
			}
		})
	}
}

func TestHTTPRefusesRedirectsAndUntrustedTLS(t *testing.T) {
	requireLinuxFiles(t)
	token := writeFixture(t, t.TempDir(), "token", "fixture-token")
	var originCalls, redirectCalls atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectCalls.Add(1)
	}))
	defer target.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originCalls.Add(1)
		http.Redirect(w, r, target.URL+"/"+synthetic, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	cfg := vaultFixture(t, origin.URL, token)
	production, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	assertAcquisition(t, production, context.Background(), vaultBinding(), "", ErrUnavailable)
	if originCalls.Load() != 0 {
		t.Fatal("unverified TLS request reached application")
	}
	assertAcquisition(t, tlsRegistry(t, origin, cfg), context.Background(), vaultBinding(), "", ErrInvalidResponse)
	if originCalls.Load() != 1 || redirectCalls.Load() != 0 {
		t.Fatal("HTTP client followed a redirect")
	}
}

func TestHTTPCancellation(t *testing.T) {
	requireLinuxFiles(t)
	token := writeFixture(t, t.TempDir(), "token", "fixture-token")
	started := make(chan struct{}, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-time.After(4 * time.Second):
			t.Error("request context was not canceled on the server")
		}
	}))
	defer srv.Close()
	r := tlsRegistry(t, srv, vaultFixture(t, srv.URL, token))
	parent, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	go func() {
		select {
		case <-started:
			cancel()
		case <-ctx.Done():
		}
	}()
	assertAcquisition(t, r, ctx, vaultBinding(), "", ErrCanceled)
	deadline, stopDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stopDeadline()
	assertAcquisition(t, r, deadline, vaultBinding(), "", ErrDeadline)
}

func TestConfigAndSelectorRefusals(t *testing.T) {
	ref := FileRef{Path: "/fixture/token"}
	for _, endpoint := range []string{"http://example.test", "https://user:password@example.test", "https://example.test/path", "https://example.test?", "https://example.test#fragment", "https://example.test:0", "https://[example.test]", "https://example.test\\outside"} {
		if _, err := New(vaultFixture(t, endpoint, ref)); err != ErrInvalidConfig {
			t.Fatal("unsafe endpoint accepted")
		}
	}
	// HTTP is allowed only at the unexported injected-transport test seam.
	if _, err := newRegistry(vaultFixture(t, "http://example.test", ref), &http.Transport{}); err != nil {
		t.Fatal("explicit HTTP test seam rejected valid origin")
	}
	for _, ref := range []FileRef{{Path: "relative/token"}, {Path: "/fixture/../token"}, {Path: "/fixture//token"}, {Path: "/fixture/token\x00"}} {
		if _, err := New(vaultFixture(t, "https://example.test", ref)); err != ErrInvalidConfig {
			t.Fatal("unsafe credential path accepted")
		}
	}
	cfg := vaultFixture(t, "https://example.test", ref)
	for _, prefix := range []string{"", "/outside", "approved/..", "approved%2Foutside"} {
		cfg.Connections["vault"].Vault.PathPrefix = prefix
		if _, err := New(cfg); err != ErrInvalidConfig {
			t.Fatal("unsafe Vault prefix accepted")
		}
	}
	cfg.Connections["vault"].Vault.PathPrefix = "approved"
	cfg.Version = SchemaVersion + 1
	if _, err := New(cfg); err != ErrInvalidConfig {
		t.Fatal("unknown config version accepted")
	}
	cfg.Version = SchemaVersion
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside", "/outside", "one//two", "one/./two", "one\\two", "one/%2e%2e", "http://example.test", "one?version=7"} {
		s := vaultBinding()
		s.Vault.Path = path
		assertAcquisition(t, r, context.Background(), s, "", ErrInvalidSelector)
	}
	s := vaultBinding()
	s.Version++
	assertAcquisition(t, r, context.Background(), s, "", ErrInvalidSelector)
	s = vaultBinding()
	s.Connection = "not-registered"
	assertAcquisition(t, r, context.Background(), s, "", ErrDenied)
	s = vaultBinding()
	s.File = &FileSelector{Source: "allowlisted"}
	assertAcquisition(t, r, context.Background(), s, "", ErrInvalidSelector)
	s.Vault = nil
	assertAcquisition(t, r, context.Background(), s, "", ErrInvalidSelector)
}

func TestValueRedaction(t *testing.T) {
	v := newValue([]byte(synthetic))
	var stringer fmt.Stringer = v
	var goStringer fmt.GoStringer = v
	if stringer.String() != redacted || goStringer.GoString() != redacted {
		t.Fatal("value did not implement redacted string methods")
	}
	for _, format := range []string{"%s", "%v", "%+v", "%#v", "%q", "%x"} {
		if got := fmt.Sprintf(format, v); got != redacted {
			t.Fatal("direct formatting was not redacted")
		}
	}
	wrapped := struct{ hidden Value }{v}
	if strings.Contains(fmt.Sprintf("%#v", wrapped), synthetic) {
		t.Fatal("recursive formatting exposed plaintext")
	}
	for _, input := range []any{v, &v, []Value{v}, struct{ Secret Value }{v}, Value{}} {
		if data, err := json.Marshal(input); len(data) != 0 || !errors.Is(err, ErrValueSerialization) {
			t.Fatal("value JSON serialization was not refused")
		}
	}
	copyOfValue := v.Bytes()
	copyOfValue[0] = '?'
	if string(v.Bytes()) != synthetic {
		t.Fatal("Bytes did not provide an independent copy")
	}
}
