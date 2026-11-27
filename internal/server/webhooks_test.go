package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/webhooks"
)

func webhookServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, "data")
	os.Mkdir(data, 0700)
	keys := filepath.Join(root, "keys")
	os.Mkdir(keys, 0700)
	keyPath := filepath.Join(keys, "webhooks.key")
	os.WriteFile(keyPath, bytes.Repeat([]byte{4}, 32), 0600)
	cfg := config.Default()
	cfg.DataDir = data
	cfg.WebhooksEnabled = true
	cfg.WebhooksDir = filepath.Join(data, "webhooks")
	cfg.WebhooksKeyFile = keyPath
	if e := InitializeWebhooks(cfg); e != nil {
		t.Fatal(e)
	}
	a, e := auth.New(auth.Config{User: "admin", Password: "fixture-only-password", APIToken: "fixture-token"})
	if e != nil {
		t.Fatal(e)
	}
	st, e := store.NewFSStore(cfg.DataDir)
	if e != nil {
		t.Fatal(e)
	}
	return New(cfg, nil, st, nil, nil, a)
}

type deniedWebhookDispatcher struct{ validations atomic.Int32 }

func (d *deniedWebhookDispatcher) Validate(context.Context, webhooks.Trigger) error {
	d.validations.Add(1)
	return webhooks.ErrUnsupported
}
func (d *deniedWebhookDispatcher) Execute(context.Context, webhooks.Trigger, webhooks.IncomingRequest) webhooks.TriggerResult {
	panic("denied action executed")
}
func TestProductionWebhookIngressKeepsOtherAuthBoundaries(t *testing.T) {
	s := webhookServer(t)
	d := &deniedWebhookDispatcher{}
	s.webhookDispatcher = d
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if e := s.prepareWebhooks(ctx); e != nil {
		t.Fatal(e)
	}
	defer s.webhooks.Close()
	tr, key, e := s.webhooks.SaveTrigger(ctx, webhooks.Trigger{Name: "denied fixture", StackID: "fixture-stack", TargetID: "fixture-target", Action: "plan", Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	body := `{"id":"fixture-one"}`
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	signature := webhooks.Sign([]byte(key), ts, []byte(body))
	path := "/hooks/" + tr.ID
	for _, tc := range []struct {
		method, path, sig string
		want              int
	}{{"POST", path, "", 401}, {"POST", path, signature, 403}, {"POST", path + "?action=deploy", signature, 401}, {"POST", path + "?", signature, 401}, {"POST", "/api/stacks", signature, 401}, {"POST", "/api/webhooks/endpoints", signature, 401}, {"GET", path, signature, 302}} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Brine-Timestamp", ts)
		r.Header.Set("Brine-Signature", tc.sig)
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	if d.validations.Load() != 1 {
		t.Fatal("only verified exact ingress should reach admission", d.validations.Load())
	}
	r := httptest.NewRequest(http.MethodGet, "/api/webhooks/", nil)
	r.Header.Set("Authorization", "Bearer fixture-token")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"health"`) {
		t.Fatal("production management not enabled", w.Code, w.Body.String())
	}
}
func TestWebhookStartupFailsClosedForBadKeysAndAliases(t *testing.T) {
	for _, mode := range []string{"missing", "insecure", "alias-in-data"} {
		t.Run(mode, func(t *testing.T) {
			s := webhookServer(t)
			switch mode {
			case "missing":
				os.Remove(s.cfg.WebhooksKeyFile)
			case "insecure":
				os.Chmod(s.cfg.WebhooksKeyFile, 0644)
			case "alias-in-data":
				path := filepath.Join(s.cfg.DataDir, "key")
				os.WriteFile(path, bytes.Repeat([]byte{1}, 32), 0600)
				alias := filepath.Join(filepath.Dir(s.cfg.DataDir), "alias")
				os.Symlink(s.cfg.DataDir, alias)
				s.cfg.WebhooksKeyFile = filepath.Join(alias, "key")
			}
			if e := s.prepareWebhooks(context.Background()); e == nil {
				if s.webhooks != nil {
					s.webhooks.Close()
				}
				t.Fatal("unsafe configuration started")
			}
			if s.webhooks != nil {
				t.Fatal("failed bootstrap published a service")
			}
		})
	}
}
func TestWebhookShutdownDrainsWorkers(t *testing.T) {
	s := webhookServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	if e := s.prepareWebhooks(ctx); e != nil {
		t.Fatal(e)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- s.webhooks.Close() }()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("webhook workers failed to drain")
	}
	if s.webhooks.Health().Delivery != "stopped" {
		t.Fatal("worker still running")
	}
}
