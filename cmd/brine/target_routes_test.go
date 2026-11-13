package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/api"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/web"
)

// Synthetic subprocess, not Dill or an engine. Its built-in endpoint is
// deliberately wrong. Every supported source command requires trusted identity.
func TestTargetDillHelper(t *testing.T) {
	sep := -1
	for i, a := range os.Args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		return
	}
	args := os.Args[sep+1:]
	socket, action, stack, kind := args[0], "", "", ""
	for i, a := range args {
		if i+1 < len(args) {
			switch a {
			case "--engine-socket":
				socket = args[i+1]
			case "-p":
				stack = args[i+1]
			case "--engine":
				kind = args[i+1]
			}
		}
		switch a {
		case "up", "down", "stop", "start", "restart", "teardown", "plan", "validate", "ps":
			action = a
		}
	}
	if stack != "demo" || (kind != "docker" && kind != "podman") {
		os.Exit(9)
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	method := "POST"
	if action == "plan" || action == "validate" || action == "ps" {
		method = "GET"
	}
	req, _ := http.NewRequest(method, "http://synthetic/target/"+action, nil)
	res, err := (&http.Client{Transport: tr, Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		os.Exit(2)
	}
	_, _ = io.Copy(os.Stdout, res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 200 {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestTwoEndpointRoutesAndAlphaBypass(t *testing.T) {
	for _, profile := range []string{"legacy", "alpha"} {
		for _, kind := range []string{"docker", "podman"} {
			t.Run(profile+"/"+kind, func(t *testing.T) { testTargetRoutes(t, profile, kind) })
		}
	}
}

func testTargetRoutes(t *testing.T, profile, kind string) {
	// Short socket paths also work on Darwin's sockaddr_un limit.
	root, err := os.MkdirTemp("/tmp", "brine-target-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	sockets := [2]string{filepath.Join(root, "wrong.sock"), filepath.Join(root, "selected.sock")}
	var mu sync.Mutex
	calls := [2][]string{}
	for i, p := range sockets {
		listener, e := net.Listen("unix", p)
		if e != nil {
			t.Fatal(e)
		}
		idx := i
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			calls[idx] = append(calls[idx], r.Method+" "+r.URL.Path)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.URL.Path == "/_ping":
				w.Header().Set("API-Version", "1.47")
				fmt.Fprint(w, "OK")
			case strings.HasSuffix(r.URL.Path, "/containers/ordinary/json"):
				fmt.Fprint(w, `{"Id":"ordinary","Name":"/ordinary","Config":{"Image":"synthetic","Tty":true,"Labels":{}},"State":{"Status":"exited"},"NetworkSettings":{"Ports":{}}}`)
			case strings.HasSuffix(r.URL.Path, "/containers/json"), strings.HasSuffix(r.URL.Path, "/images/json"):
				fmt.Fprint(w, `[]`)
			case strings.HasSuffix(r.URL.Path, "/containers/ordinary/logs"):
				fmt.Fprint(w, "synthetic log\n")
			case r.Method == "DELETE" && strings.Contains(r.URL.Path, "/images/"):
				fmt.Fprint(w, `[]`)
			case r.URL.Path == "/target/plan":
				fmt.Fprint(w, `{"changes":[],"summary":{"create":0,"recreate":0,"remove":0,"noop":0}}`)
			case r.URL.Path == "/target/validate":
				fmt.Fprint(w, `{"valid":true}`)
			case r.URL.Path == "/target/ps":
				fmt.Fprint(w, `[]`)
			case strings.HasPrefix(r.URL.Path, "/target/"):
				fmt.Fprint(w, `{}`)
			default:
				w.WriteHeader(204)
			}
		})}
		go srv.Serve(listener)
		t.Cleanup(func() { _ = srv.Close() })
	}
	t.Setenv("DOCKER_HOST", "unix://"+sockets[0])
	t.Setenv("PODMAN_SOCKET", sockets[0])
	cfg := config.Default()
	cfg.Engine = kind
	cfg.Profile = profile
	if profile == "alpha" && cfg.Validate() == nil {
		t.Fatal("Alpha accepted ambient endpoint")
	}
	cfg.EngineSocket = sockets[1]
	if profile == "alpha" {
		cfg.NoAuth = true
		if cfg.Validate() == nil {
			t.Fatal("Alpha accepted unauthenticated startup")
		}
		cfg.NoAuth = false
	}
	if err = cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	st, err := store.NewFSStore(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	// Recipe name must never replace the store's demo alias as deployment identity.
	if _, err = st.Create("demo", `config { engine = "`+kind+`"; name = "wrong-recipe-target" }`); err != nil {
		t.Fatal(err)
	}
	testBin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "fake-dill")
	if err = os.WriteFile(script, []byte("#!/bin/sh\nexec '"+testBin+"' -test.run='^TestTargetDillHelper$' -- '"+sockets[0]+"' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runner := dill.New(script)
	runner.Profile = profile
	runner.Engine = kind
	runner.Socket = strings.TrimPrefix(effectiveEngineHost(cfg), "unix://")
	runner.ResolveStack = stackPathResolver(st)
	runner.CheckSource = stackSourceCheck(st)
	eng, err := engine.NewDockerEngineWithHost(effectiveEngineHost(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	runner.BeforeLegacyAction = legacySecretGate(st, eng, nil)
	ag := agent.NewLocalAgent(eng, runner)
	authn, err := auth.New(auth.Config{User: "admin", Password: "synthetic-target-password", APIToken: "synthetic-target-token"})
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(authn.Middleware)
	api.Mount(router, ag, st, nil, nil, kind, httpx.Timeouts{}, 10*time.Second)
	web.Mount(router, web.New(ag, st, nil, nil, kind, false, authn, 10*time.Second), httpx.Timeouts{})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", "Bearer synthetic-target-token")
		router.ServeHTTP(w, req)
		return w
	}
	// Alpha cleanup is still a mutation and must not be accessible without
	// authentication merely because it bypasses source/secret negotiation.
	for _, action := range []string{"stop", "down", "up"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/api/stacks/demo/"+action, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: %d %s", action, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/stacks/demo", "/containers/ordinary", "/images"} {
		w := call("GET", path, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), sockets[1]) || strings.Contains(w.Body.String(), sockets[0]) || !strings.Contains(w.Body.String(), "configured only") {
			t.Fatalf("identity %s: %d %s", path, w.Code, w.Body.String())
		}
		if profile == "alpha" && !strings.Contains(w.Body.String(), "target/ownership contract unavailable") {
			t.Fatalf("missing disabled reason: %s", path)
		}
		if path == "/stacks/demo" {
			html := w.Body.String()
			for _, action := range []string{"up", "start", "restart", "teardown", "stop", "down"} {
				needle := "runAction('" + action + "', 'demo')"
				at := strings.Index(html, needle)
				if at < 0 {
					t.Fatalf("missing action %s", action)
				}
				start := strings.LastIndex(html[:at], "<button ")
				end := strings.Index(html[at:], "</button>")
				if start < 0 || end < 0 {
					t.Fatalf("missing button %s", action)
				}
				button := html[start : at+end]
				blocked := profile == "alpha" && action != "stop" && action != "down"
				if strings.Contains(button, "disabled") != blocked {
					t.Fatalf("incorrect disabled state for %s: %s", action, button)
				}
			}
		}
	}
	for _, action := range []string{"up", "start", "restart", "teardown", "stop", "down"} {
		w := call("POST", "/api/stacks/demo/"+action, "")
		blocked := profile == "alpha" && action != "stop" && action != "down"
		if blocked {
			if !strings.Contains(w.Body.String(), "[brine:error]") || !strings.Contains(w.Body.String(), "Alpha mutations unavailable") {
				t.Fatalf("Alpha bypass %s: %d %s", action, w.Code, w.Body.String())
			}
		} else if w.Code != 200 || !strings.Contains(w.Body.String(), "[brine:done]") {
			t.Fatalf("lifecycle %s: %d %s", action, w.Code, w.Body.String())
		}
	}
	for _, p := range []string{"/api/stacks/demo/plan", "/web/stacks/demo/plan"} {
		w := call("GET", p, "")
		if profile == "alpha" {
			if !strings.Contains(w.Body.String(), "Alpha mutations unavailable") {
				t.Fatalf("plan bypass: %s", w.Body.String())
			}
		} else if w.Code != 200 {
			t.Fatalf("plan: %d %s", w.Code, w.Body.String())
		}
	}
	for _, p := range []string{"/api/containers/ordinary/start", "/api/containers/ordinary/stop", "/api/containers/ordinary/restart", "/web/containers/ordinary/start", "/web/containers/ordinary/stop", "/web/containers/ordinary/restart", "/api/images/pull", "/web/images/pull", "/api/images/synthetic?force=true"} {
		method, body := "POST", ""
		if strings.Contains(p, "images/pull") {
			body = `{"ref":"synthetic:only"}`
			if strings.HasPrefix(p, "/web") {
				body = "ref=synthetic%3Aonly"
			}
		} else if strings.Contains(p, "images/") {
			method = "DELETE"
		}
		w := call(method, p, body)
		if profile == "alpha" {
			if !strings.Contains(w.Body.String(), "Alpha mutations unavailable") {
				t.Fatalf("direct bypass %s: %d %s", p, w.Code, w.Body.String())
			}
		} else if w.Code >= 400 {
			t.Fatalf("legacy direct %s: %d %s", p, w.Code, w.Body.String())
		}
	}
	for _, route := range []struct{ method, path, body string }{{"GET", "/api/stacks/demo/validate", ""}, {"POST", "/web/stacks/demo/validate", "pkl_content=synthetic"}, {"POST", "/web/stacks/demo/validate", ""}, {"GET", "/web/stacks/demo/containers", ""}, {"GET", "/api/containers", ""}, {"GET", "/api/images", ""}, {"GET", "/api/containers/ordinary/logs", ""}, {"GET", "/web/containers/ordinary/logs", ""}} {
		w := call(route.method, route.path, route.body)
		if profile == "alpha" && strings.Contains(route.path, "/validate") {
			if w.Code != 200 || (!strings.Contains(w.Body.String(), "unsupported") && !strings.Contains(w.Body.String(), "contract") && !strings.Contains(w.Body.String(), "Alpha mutations unavailable") && !strings.Contains(w.Body.String(), "operation unavailable or incomplete")) {
				t.Fatalf("unconfigured Alpha validation did not fail closed: %d %s", w.Code, w.Body.String())
			}
		} else if w.Code != 200 || strings.Contains(w.Body.String(), "validation failed") {
			t.Fatalf("read %s: %d %s", route.path, w.Code, w.Body.String())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls[0]) != 0 {
		t.Fatalf("wrong endpoint requests (including writes): %v", calls[0])
	}
	counts := map[string]int{}
	for _, c := range calls[1] {
		counts[c]++
		if profile == "alpha" && (strings.HasPrefix(c, "POST ") || strings.HasPrefix(c, "DELETE ")) && c != "POST /target/stop" && c != "POST /target/down" {
			t.Fatalf("Alpha write: %s", c)
		}
	}
	for _, c := range []string{"POST /target/stop", "POST /target/down"} {
		if counts[c] == 0 {
			t.Fatalf("selected operation absent %s: %v", c, calls[1])
		}
	}
	wantValidations := 3
	if profile == "alpha" {
		wantValidations = 0
	}
	if counts["GET /target/validate"] != wantValidations {
		t.Fatalf("saved/stdin validation calls: %v", counts)
	}
	if profile == "legacy" && counts["POST /target/up"] != 1 {
		t.Fatal("legacy deployment not exercised")
	}
	t.Logf("selected=%d wrong=0 requests; profile=%s", len(calls[1]), profile)
}
