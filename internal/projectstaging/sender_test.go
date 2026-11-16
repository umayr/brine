package projectstaging

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/projectreview"
	"github.com/umayr/brine/internal/projectsnapshot"
)

func TestConfirmedLocalStage(t *testing.T) {
	var uploads atomic.Int32
	var received []byte
	sock := filepath.Join(t.TempDir(), "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			io.WriteString(w, `{"api_version":"v1","capabilities":["health","status","snapshot-staging"]}`)
		case "/v1/status":
			io.WriteString(w, `{"stack":"stack","engine":"docker","containers":[]}`)
		case "/v1/snapshots":
			uploads.Add(1)
			received, _ = io.ReadAll(r.Body)
			w.WriteHeader(201)
			io.WriteString(w, `{"status":"staged","stage_id":"0123456789abcdef0123456789abcdef"}`)
		default:
			w.WriteHeader(404)
		}
	})}
	defer server.Close()
	go server.Serve(l)
	store, _ := projectreview.New(10, 1<<20, time.Minute)
	defer store.Close()
	current := projectreview.Current{Stack: "stack", Target: projectreview.Target{Kind: projectreview.Local, Socket: sock, Identity: "target"}, Options: map[string]string{"engine": "docker"}}
	sender, err := New(store, dillserve.Target{Socket: sock, Stack: "stack", Engine: "docker"}, "target", func(context.Context) (projectreview.Current, error) { return current, nil })
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("captured")
	snap := projectsnapshot.Snapshot{Entrypoint: "main.pkl", Files: []projectsnapshot.File{{Path: "main.pkl", Content: content, SHA256: sha256.Sum256(content)}}}
	p, err := sender.Create(context.Background(), "session", snap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sender.Stage(context.Background(), "session", p.ID, ""); err == nil || uploads.Load() != 0 {
		t.Fatal("unconfirmed upload")
	}
	token, err := sender.Confirm(context.Background(), "session", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	content[0] = 'X'
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := sender.Stage(context.Background(), "session", p.ID, token); e == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 || uploads.Load() != 1 {
		t.Fatal("one-shot violated")
	}
	var body struct {
		Files []struct {
			Bytes []byte `json:"bytes"`
		} `json:"files"`
	}
	if json.Unmarshal(received, &body) != nil || len(body.Files) != 1 || string(body.Files[0].Bytes) != "captured" {
		t.Fatal("wrong captured bytes")
	}
}
func TestUnsupportedAndMismatch(t *testing.T) {
	store, _ := projectreview.New(4, 1<<20, time.Minute)
	defer store.Close()
	c := projectreview.Current{Stack: "stack", Target: projectreview.Target{Kind: projectreview.Local, Socket: "/tmp/staging.sock", Identity: "target"}, Options: map[string]string{"engine": "docker"}}
	s, _ := New(store, dillserve.Target{Socket: c.Target.Socket, Stack: "stack", Engine: "docker"}, "target", func(context.Context) (projectreview.Current, error) { return c, nil })
	b := []byte("x")
	snap := projectsnapshot.Snapshot{Entrypoint: "x", Files: []projectsnapshot.File{{Path: "x", Content: b, SHA256: sha256.Sum256(b)}}}
	p, e := s.Create(context.Background(), "s", snap)
	if e != nil {
		t.Fatal(e)
	}
	c.Stack = "changed"
	if _, e = s.Confirm(context.Background(), "s", p.ID); e == nil {
		t.Fatal("mismatch accepted")
	}
	c.Stack = "stack"
	if _, e = s.Confirm(context.Background(), "s", p.ID); e == nil {
		t.Fatal("mismatch not revoked")
	}
	c.Target.Kind = projectreview.SSH
	if _, e = s.Create(context.Background(), "s", snap); !errors.Is(e, ErrUnsupported) {
		t.Fatal("ssh not rejected")
	}
}
