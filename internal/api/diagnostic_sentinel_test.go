//go:build unix

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/secretbindings"
	"github.com/umayr/brine/internal/store"
)

// Reference uploads must reject plaintext without echoing/storing it. Strict
// malformed diagnostics are suppressed, whereas a VALID structured plan's
// deployment values are operator-visible and privately persisted by design.
func TestDiagnosticSentinelStrictAPIAndPlaintextBoundary(t *testing.T) {
	const login = "synthetic-forbidden-api-login"
	const token = "synthetic-forbidden-api-token"
	const plaintext = "synthetic-forbidden-uploaded-plaintext"
	const diagnostic = "synthetic-deployment-api-diagnostic"
	t.Setenv("BRINE_AUTH_PASSWORD", login)
	t.Setenv("BRINE_API_TOKEN", token)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, ".brine-api-diagnostic-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	workspace, private := filepath.Join(dir, "workspace"), filepath.Join(dir, "private")
	if err = os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	st, err := store.NewFSStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Create("demo", "x"); err != nil {
		t.Fatal(err)
	}
	state, err := secretbindings.Open(private, workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	calls, mode, bin := filepath.Join(dir, "calls"), filepath.Join(dir, "mode"), filepath.Join(dir, "dill")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
printf '%%s\n' "$BRINE_AUTH_PASSWORD" "$BRINE_API_TOKEN" >&2
printf 'synthetic-deployment-api-diagnostic-stderr\n' >&2
case " $* " in *" secret-capabilities "*) echo '{"contract":"dill.alpha-secrets/1","bindings":"dill.secret-bindings/1","policy":"dill.secret-policy/1","engines":["docker-host-file"]}';exit 0;; esac
if [ "$(cat %q)" = failure ]; then echo 'synthetic-deployment-api-diagnostic-stdout';exit 2;fi
case " $* " in
 *" plan "*) while [ "$#" -gt 0 ];do if [ "$1" = --proof-file ];then shift;proof="$1";fi;shift;done
 echo '{"contract":"dill.alpha-secrets-preview/1","proof":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"}' > "$proof";chmod 600 "$proof"
 echo '{"changes":[{"service":"web","kind":"create","diffs":[{"field":"deployment","before":"","after":"synthetic-deployment-api-diagnostic"}]}],"summary":{"create":1,"recreate":0,"remove":0,"noop":0}}';exit 1;;
 *" validate "*) echo '{"valid":true}';exit 0;;
 *" secrets check "*) echo '{"contract":"dill.alpha-secrets/1","status":"available"}';exit 0;;
 *" up "*) echo 'synthetic-deployment-api-diagnostic-stdout';exit 0;;
esac
exit 9
`, calls, mode)
	if err = os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	setMode := func(m string) {
		t.Helper()
		if err := os.WriteFile(mode, []byte(m), 0600); err != nil {
			t.Fatal(err)
		}
	}
	setMode("valid")
	runner := dill.New(bin)
	sr := &dill.SecretRunner{Runner: runner, State: state, Coordinator: &runner.Coordinator, Engine: "docker", Socket: "/tmp/synthetic.sock", Policy: "/tmp/synthetic-policy", Target: "lab-a"}
	runner.StrictActions = &dill.StrictController{Secret: sr, Resolve: func(string) (string, error) { return "demo", nil }, LegacyGate: func(context.Context, string) error { return nil }}
	a, err := auth.New(auth.Config{User: "admin", Password: login, APIToken: token})
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(a.Middleware)
	Mount(r, agent.NewLocalAgent(nil, runner), st, nil, nil, "docker", httpx.Timeouts{Default: time.Minute, Lifecycle: time.Minute}, time.Minute)
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	call := func(method, path, body string, authorized bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Accept", "application/json")
		if authorized {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		for _, forbidden := range []string{login, token, plaintext} {
			if strings.Contains(w.Body.String(), forbidden) {
				t.Fatal("forbidden input reached API sink")
			}
		}
		return w
	}
	base := "/api/stacks/demo/secret-bindings"
	for _, row := range []struct{ method, path string }{{"GET", base}, {"PUT", base}, {"POST", base + "/status"}, {"POST", base + "/preview"}, {"POST", base + "/confirm"}, {"POST", "/api/stacks/demo/up"}, {"GET", "/api/stacks/demo/validate"}} {
		w := call(row.method, row.path, `{"value":"`+plaintext+`"}`, false)
		if w.Code != 401 {
			t.Fatal("unauthorized strict route accepted")
		}
		if _, err = os.Stat(calls); !os.IsNotExist(err) {
			t.Fatal("unauthorized request reached subprocess producer")
		}
	}
	doc := `{"version":"dill.secret-bindings/1","target":"lab-a","stack":"demo","bindings":[{"name":"db","source":"synthetic-source","revision":"r1"}]}`
	bad := strings.Replace(doc, `"revision":"r1"`, `"revision":"r1","value":"`+plaintext+`"`, 1)
	if w := call("PUT", base, bad, true); w.Code != 400 {
		t.Fatal("plaintext binding upload accepted")
	}
	if _, err = state.Draft(secretbindings.DeploymentID("demo"), "demo", "lab-a"); !os.IsNotExist(err) {
		t.Fatal("rejected plaintext created draft")
	}
	if w := call("PUT", base, doc, true); w.Code != 200 {
		t.Fatal("valid reference upload rejected")
	}
	setMode("failure")
	for _, row := range []struct {
		method, path string
		code         int
	}{{"POST", base + "/preview", 400}, {"POST", base + "/status", 200}, {"GET", "/api/stacks/demo/validate", 200}, {"POST", "/api/stacks/demo/up", 200}} {
		w := call(row.method, row.path, "", true)
		if w.Code != row.code || strings.Contains(w.Body.String(), diagnostic) {
			t.Fatal("strict raw diagnostic reached API sink")
		}
	}
	setMode("valid")
	w := call("POST", base+"/preview", "", true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), diagnostic) || strings.Contains(w.Body.String(), diagnostic+"-stderr") {
		t.Fatal("valid plan/deployment value boundary not exercised")
	}
	var preview struct {
		Token string `json:"preview_token"`
	}
	if json.Unmarshal(w.Body.Bytes(), &preview) != nil || preview.Token == "" {
		t.Fatal("preview token missing")
	}
	w = call("POST", base+"/confirm", `{"preview_token":"`+preview.Token+`"}`, true)
	if w.Code != 200 || strings.Contains(w.Body.String(), diagnostic) {
		t.Fatal("strict up raw diagnostic reached confirmation response")
	}
	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{" plan ", " validate ", " up ", " secrets check "} {
		if !strings.Contains(string(b), command) {
			t.Fatal("sentinel producer action not executed")
		}
	}
	for _, forbidden := range []string{login, token, plaintext} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatal("forbidden input reached service log")
		}
	}
	// Check actual persisted bytes, not only the parsed API projection.
	err = filepath.WalkDir(workspace, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		for _, forbidden := range []string{login, token, plaintext} {
			if bytes.Contains(b, []byte(forbidden)) {
				t.Fatal("forbidden input persisted in workspace")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(private, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		for _, forbidden := range []string{login, token, plaintext} {
			if bytes.Contains(b, []byte(forbidden)) {
				t.Fatal("forbidden input persisted in private state")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
