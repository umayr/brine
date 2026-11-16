//go:build darwin || linux

package dillserve

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestRealDillServeInterop uses only a fake local Docker API; it never contacts a daemon.
func TestRealDillServeInterop(t *testing.T) {
	bin := os.Getenv("DILL_SERVE_TEST_BIN")
	if bin == "" {
		t.Skip("set DILL_SERVE_TEST_BIN to a locally built dill-serve binary")
	}
	if !filepath.IsAbs(bin) {
		t.Fatal("DILL_SERVE_TEST_BIN must be absolute")
	}
	base, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "dsi-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dockerSock, serveSock := filepath.Join(dir, "docker.sock"), filepath.Join(dir, "serve.sock")
	l, err := net.Listen("unix", dockerSock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	mode := "normal"
	var unexpected []string
	var lists, inspects int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		p := r.URL.Path
		if r.Method != "GET" && !(r.Method == "HEAD" && p == "/_ping") {
			unexpected = append(unexpected, r.Method+" "+p)
			http.Error(w, "forbidden", 405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("API-Version", "1.44")
		switch {
		case p == "/_ping":
			w.Write([]byte("OK"))
		case p == "/version":
			w.Write([]byte(`{"ApiVersion":"1.44","MinAPIVersion":"1.24"}`))
		case strings.HasSuffix(p, "/containers/json"):
			lists++
			var filters map[string]map[string]bool
			if json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters) != nil || r.URL.Query().Get("all") != "1" || !(len(filters) == 1 && ((len(filters["label"]) == 2 && filters["label"]["dill.managed=true"] && filters["label"]["dill.stack=demo"]) || (len(filters["id"]) == 1 && filters["id"]["0123456789abcdef"]))) {
				unexpected = append(unexpected, "bad list query: "+r.URL.RawQuery)
			}
			if mode == "failure" {
				http.Error(w, `{"message":"synthetic engine failure"}`, 500)
				return
			}
			image := "example:latest"
			if mode == "large" {
				image = strings.Repeat("x", maxResponse+100)
			}
			json.NewEncoder(w).Encode([]map[string]any{{"Id": "0123456789abcdef", "Names": []string{"/demo-web"}, "Image": image, "State": "running", "Status": "Up", "Ports": []any{}}})
		case strings.HasSuffix(p, "/containers/demo-web/json"):
			inspects++
			w.Write([]byte(`{"Id":"0123456789abcdef"}`))
		default:
			unexpected = append(unexpected, r.Method+" "+p)
			http.Error(w, "unexpected", 404)
		}
	})
	server := &http.Server{Handler: handler}
	go server.Serve(l)
	defer server.Close()
	cmd := exec.Command(bin, "-engine", "docker", "-endpoint", "unix://"+dockerSock, "-stack", "demo", "-socket", serveSock)
	// Avoid inheriting engine and proxy settings; the binary receives only the explicit endpoint.
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	client, err := NewLocal(Target{Socket: serveSock, Stack: "demo", Engine: "docker"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err = client.Health(context.Background())
		if err == nil {
			break
		}
		select {
		case e := <-done:
			stopped = true
			t.Fatalf("server exited: %v: %s", e, output.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("server not ready: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	h, err := client.Health(context.Background())
	if err != nil || h.APIVersion != "v1" {
		t.Fatalf("health: %+v %v", h, err)
	}
	s, err := client.Status(context.Background())
	if err != nil || s.Stack != "demo" || s.Engine != "docker" || len(s.Containers) != 1 {
		t.Fatalf("status: %+v %v", s, err)
	}
	var c struct{ Name, State, Image string }
	if json.Unmarshal(s.Containers[0], &c) != nil || c.Name != "demo-web" || c.State != "running" || c.Image != "example:latest" {
		t.Fatalf("container: %s", s.Containers[0])
	}
	for _, target := range []Target{{Socket: serveSock, Stack: "wrong", Engine: "docker"}, {Socket: serveSock, Stack: "demo", Engine: "podman"}} {
		other, _ := NewLocal(target)
		_, err := other.Status(context.Background())
		other.Close()
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("target mismatch: %v", err)
		}
	}
	for _, tc := range []struct {
		mode   string
		status int
		code   string
	}{{"failure", 502, "engine_error"}, {"large", 500, "response_too_large"}} {
		mu.Lock()
		mode = tc.mode
		mu.Unlock()
		_, err := client.Status(context.Background())
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s status: %v", tc.mode, err)
		}
		resp, err := client.http.Get("http://dillserve/v1/status")
		if err != nil {
			t.Fatalf("%s raw status: %v", tc.mode, err)
		}
		var body struct {
			Code string `json:"code"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != tc.status || body.Code != tc.code {
			t.Errorf("%s raw status: HTTP %d code %q decode %v", tc.mode, resp.StatusCode, body.Code, err)
		}
	}
	mu.Lock()
	if lists < 5 || inspects < 4 || len(unexpected) > 0 {
		t.Errorf("fake API lists=%d inspects=%d unexpected=%v", lists, inspects, unexpected)
	}
	mu.Unlock()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		stopped = true
		if err != nil {
			t.Fatalf("shutdown: %v (%s)", err, output.String())
		}
	case <-time.After(7 * time.Second):
		t.Fatal("shutdown timed out")
	}
	if _, err := os.Lstat(serveSock); !os.IsNotExist(err) {
		t.Errorf("socket not removed after shutdown: %v", err)
	}
	if _, err := client.Health(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("health after shutdown: %v", err)
	}
}
