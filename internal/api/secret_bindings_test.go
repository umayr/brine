//go:build unix

package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
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

func TestSecretBindingAPIReviewAndAuthentication(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, ".brine-api-secret-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	workspace := filepath.Join(dir, "workspace")
	private := filepath.Join(dir, "private")
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
	live := filepath.Join(dir, "changed")
	fail := filepath.Join(dir, "fail")
	bin := filepath.Join(dir, "dill")
	script := fmt.Sprintf(`#!/bin/sh
case " $* " in
 *" secret-capabilities "*) echo '{"contract":"dill.alpha-secrets/1","bindings":"dill.secret-bindings/1","policy":"dill.secret-policy/1","engines":["docker-host-file"]}';exit 0;;
 *" secrets check "*) echo '{"contract":"dill.alpha-secrets/1","status":"available"}';exit 0;;
 *" plan "*) while [ "$#" -gt 0 ]; do if [ "$1" = "--proof-file" ]; then shift; p="$1"; fi; shift; done
 if [ -e '%s' ]; then proof=AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI; else proof=AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE; fi
 printf '{"contract":"dill.alpha-secrets-preview/1","proof":"%%s"}' "$proof" > "$p";chmod 600 "$p";echo '{"changes":[{"service":"web","kind":"create","diffs":[]}],"summary":{"create":1,"recreate":0,"remove":0,"noop":0}}';exit 1;;
 *" up "*) if [ -e '%s' ];then exit 1;fi;exit 0;;
esac
exit 2
`, live, fail)
	if err = os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	runner := dill.New(bin)
	sr := &dill.SecretRunner{Runner: runner, State: state, Coordinator: &runner.Coordinator, Engine: "docker", Socket: "/tmp/synthetic.sock", Policy: "/tmp/synthetic-policy", Target: "lab-a"}
	runner.StrictActions = &dill.StrictController{Secret: sr}
	ag := agent.NewLocalAgent(nil, runner)
	a, err := auth.New(auth.Config{User: "admin", Password: "password", APIToken: "synthetic-token"})
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(a.Middleware)
	r.Post("/login", a.HandleLogin)
	Mount(r, ag, st, nil, nil, "docker", httpx.Timeouts{Default: time.Minute, Lifecycle: time.Minute}, time.Minute)
	call := func(method, path, body string, authorize bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Accept", "application/json")
		if authorize {
			req.Header.Set("Authorization", "Bearer synthetic-token")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if strings.Contains(w.Body.String(), "AQEBAQEBAQEBAQE") || strings.Contains(w.Body.String(), private) || strings.Contains(w.Body.String(), "synthetic-secret-value") {
			t.Fatal("private value or path leaked")
		}
		return w
	}
	base := "/api/stacks/demo/secret-bindings"
	if w := call("GET", base, "", false); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauth: %d", w.Code)
	}
	login := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=password"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lw := httptest.NewRecorder()
	r.ServeHTTP(lw, login)
	cookies := lw.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login failed")
	}
	csrfReq := httptest.NewRequest("PUT", base, strings.NewReader("{}"))
	csrfReq.AddCookie(cookies[0])
	cw := httptest.NewRecorder()
	r.ServeHTTP(cw, csrfReq)
	if cw.Code != http.StatusForbidden {
		t.Fatalf("csrf: %d", cw.Code)
	}
	doc := `{"version":"dill.secret-bindings/1","target":"lab-a","stack":"demo","bindings":[{"name":"db","source":"db-prod","revision":"r1"}]}`
	if w := call("PUT", base, doc, true); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call("PUT", base, strings.Replace(doc, `"revision":"r1"`, `"revision":"r1","value":"synthetic-secret-value"`, 1), true); w.Code == 200 {
		t.Fatal("value upload accepted")
	}
	if w := call("POST", base+"/status", "", true); w.Code != 200 || !strings.Contains(w.Body.String(), "available") {
		t.Fatalf("status: %s", w.Body.String())
	}
	preview := func() string {
		w := call("POST", base+"/preview", "", true)
		if w.Code != 200 {
			t.Fatalf("preview: %s", w.Body.String())
		}
		var out struct {
			Token string `json:"preview_token"`
		}
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Token) != 43 {
			t.Fatal("bad public token")
		}
		return out.Token
	}
	confirm := func(token string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"preview_token": token})
		return call("POST", base+"/confirm", string(b), true)
	}
	token := preview()
	if _, err = state.Active(secretbindings.DeploymentID("demo"), "demo", "lab-a"); !os.IsNotExist(err) {
		t.Fatal("preview activated draft")
	}
	if err = os.WriteFile(live, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := confirm(token); w.Code != http.StatusConflict {
		t.Fatalf("changed proof: %d %s", w.Code, w.Body.String())
	}
	os.Remove(live)
	old := preview()
	latest := preview()
	if w := confirm(old); w.Code != http.StatusConflict {
		t.Fatal("old token accepted")
	}
	if w := confirm(latest); w.Code != 200 {
		t.Fatalf("confirmation: %s", w.Body.String())
	}
	newer := strings.Replace(doc, `"revision":"r1"`, `"revision":"r2"`, 1)
	if w := call("PUT", base, newer, true); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	active, err := state.Active(secretbindings.DeploymentID("demo"), "demo", "lab-a")
	if err != nil || !bytes.Contains(active, []byte(`"revision":"r1"`)) {
		t.Fatal("draft rotated active")
	}
	token = preview()
	if err = os.WriteFile(fail, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := confirm(token); w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "partial") {
		t.Fatalf("apply failure: %s", w.Body.String())
	}
	active, err = state.Active(secretbindings.DeploymentID("demo"), "demo", "lab-a")
	if err != nil || !bytes.Contains(active, []byte(`"revision":"r2"`)) {
		t.Fatal("approved revision not retained")
	}
}
