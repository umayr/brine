//go:build linux

package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/snapshothistory"
)

func TestMetadataStartupDenialsReleaseArchive(t *testing.T) {
	for _, mode := range []string{"stale-pin", "partial-pins", "wrong-store", "missing-key", "unsafe-key", "workspace-alias", "key-alias", "missing-state"} {
		t.Run(mode, func(t *testing.T) {
			s, cfg := metadataStartupFixture(t)
			options, _, err := cfg.RestartMetadataStartup()
			if err != nil {
				t.Fatal(err)
			}
			if err := snapshothistory.Initialize(context.Background(), options); err != nil {
				t.Fatal(err)
			}
			settings := *cfg
			settings.RestartMetadata.Enrollments = append([]string(nil), cfg.RestartMetadata.Enrollments...)
			switch mode {
			case "stale-pin":
				settings.RestartMetadata.Enrollments = []string{"demo=" + strings.Repeat("ef", 16)}
			case "partial-pins":
				settings.RestartMetadata.Enrollments = append(settings.RestartMetadata.Enrollments, "missing="+strings.Repeat("ef", 16))
			case "wrong-store":
				settings.RestartMetadata.StoreID = strings.Repeat("cd", 32)
			case "missing-key":
				settings.RestartMetadata.KeyFile = filepath.Join(filepath.Dir(options.KeyFile), "absent")
			case "unsafe-key":
				if err := os.Chmod(options.KeyFile, 0644); err != nil {
					t.Fatal(err)
				}
			case "workspace-alias":
				alias := filepath.Join(filepath.Dir(cfg.DataDir), "alias")
				if err := os.Symlink(options.ArchiveDir, alias); err != nil {
					t.Fatal(err)
				}
				settings.DataDir = alias
			case "key-alias":
				alias := filepath.Join(filepath.Dir(cfg.DataDir), "alias")
				if err := os.Link(options.KeyFile, alias); err != nil {
					t.Fatal(err)
				}
				settings.RemoteRevealSeedPath = alias
			case "missing-state":
				settings.OperationStateDir = filepath.Join(filepath.Dir(cfg.DataDir), "absent")
			}
			s.cfg = &settings
			w := s.beginRestartMetadata(context.Background())
			select {
			case <-w.done:
			case <-time.After(3 * time.Second):
				w.cutoff()
				t.Fatal("denied startup did not drain")
			}
			if err := w.stop(time.Second); err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			w.ServeHTTP(rr, httptest.NewRequest("GET", "/remote/archive/metadata?host=demo", nil))
			if rr.Code != 503 {
				t.Fatal("denied startup served")
			}
			if mode == "unsafe-key" {
				if err := os.Chmod(options.KeyFile, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "key-alias" {
				if err := os.Remove(settings.RemoteRevealSeedPath); err != nil {
					t.Fatal(err)
				}
			}
			reopened, err := snapshothistory.Open(context.Background(), options)
			if err != nil {
				t.Fatalf("failed constructor retained lock or damaged archive: %v", err)
			}
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMetadataDefaultOffIgnoresFilesystem(t *testing.T) {
	s := &Server{cfg: &config.Config{DataDir: "\x00", RemoteRegistryDir: "\x00"}}
	if w := s.beginRestartMetadata(context.Background()); w != nil {
		t.Fatal("default off started worker")
	}
}

func TestMetadataRunListenerAndShutdown(t *testing.T) {
	s, cfg := metadataStartupFixture(t)
	options, _, err := cfg.RestartMetadataStartup()
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshothistory.Initialize(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, cfg.Port, err = net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	cfg.Bind = "127.0.0.1"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("Run did not stop")
			}
		}
	}()
	// Login against the real Run listener, without reading mutable server fields.
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base := "http://127.0.0.1:" + cfg.Port
	var cookie *http.Cookie
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Post(base+"/login", "application/x-www-form-urlencoded", strings.NewReader("user=admin&password=generated-password"))
		if err == nil {
			resp.Body.Close()
			if len(resp.Cookies()) == 1 {
				cookie = resp.Cookies()[0]
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if cookie == nil {
		t.Fatal("Run listener login unavailable")
	}
	request := func(withCookie bool, method string) int {
		req, _ := http.NewRequest(method, base+"/remote/archive/metadata?host=demo", nil)
		if withCookie {
			req.AddCookie(cookie)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for request(true, "GET") == 503 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if code := request(true, "GET"); code != 200 {
		t.Fatalf("authenticated route: %d", code)
	}
	if code := request(false, "GET"); code == 200 {
		t.Fatal("anonymous metadata")
	}
	if code := request(true, "POST"); code == 200 {
		t.Fatal("metadata POST admitted")
	}
	cancel()
	select {
	case err := <-done:
		joined = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown timeout")
	}
	reopened, err := snapshothistory.Open(context.Background(), options)
	if err != nil {
		t.Fatal("Run shutdown retained archive lock")
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}
