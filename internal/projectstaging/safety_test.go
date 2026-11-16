package projectstaging

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/projectreview"
	"github.com/umayr/brine/internal/projectsnapshot"
)

func safetySnapshot(data []byte) projectsnapshot.Snapshot {
	return projectsnapshot.Snapshot{Entrypoint: "main.pkl", Files: []projectsnapshot.File{{Path: "main.pkl", Content: data, SHA256: sha256.Sum256(data)}}}
}
func safetySender(t *testing.T, sock string, ttl time.Duration) (*Sender, *projectreview.Current) {
	t.Helper()
	store, e := projectreview.New(4, 32<<20, ttl)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(store.Close)
	c := &projectreview.Current{Stack: "stack", Target: projectreview.Target{Kind: projectreview.Local, Socket: sock, Identity: "id"}, Options: map[string]string{"engine": "docker"}}
	s, e := New(store, dillserve.Target{Socket: sock, Stack: "stack", Engine: "docker"}, "id", func(context.Context) (projectreview.Current, error) { return *c, nil })
	if e != nil {
		t.Fatal(e)
	}
	return s, c
}
func approved(t *testing.T, s *Sender) (string, string) {
	t.Helper()
	p, e := s.Create(context.Background(), "session", safetySnapshot([]byte("x")))
	if e != nil {
		t.Fatal(e)
	}
	token, e := s.Confirm(context.Background(), "session", p.ID)
	if e != nil {
		t.Fatal(e)
	}
	return p.ID, token
}
func TestSSHDriftRevokes(t *testing.T) {
	s, c := safetySender(t, "/tmp/not-a-real-socket", time.Minute)
	id, token := approved(t, s)
	c.Target.Kind = projectreview.SSH
	if _, e := s.Stage(context.Background(), "session", id, token); !errors.Is(e, ErrUnsupported) {
		t.Fatalf("expected unsupported: %v", e)
	}
	c.Target.Kind = projectreview.Local
	if _, e := s.Stage(context.Background(), "session", id, token); !errors.Is(e, ErrInvalid) {
		t.Fatalf("approval survived drift: %v", e)
	}
}
func TestExpiryAndTokenTamper(t *testing.T) {
	s, _ := safetySender(t, "/tmp/not-a-real-socket", time.Second)
	id, token := approved(t, s)
	bad := "0" + token[1:]
	if bad == token {
		bad = "1" + token[1:]
	}
	if _, e := s.Stage(context.Background(), "session", id, bad); !errors.Is(e, ErrInvalid) {
		t.Fatal("bad token accepted")
	}
	time.Sleep(1100 * time.Millisecond)
	if _, e := s.Stage(context.Background(), "session", id, token); !errors.Is(e, ErrInvalid) {
		t.Fatal("expired approval accepted")
	}
}
func TestCapabilityWithdrawalPreventsUpload(t *testing.T) {
	l, err := net.Listen("unix", filepath.Join(t.TempDir(), "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var health, uploads atomic.Int32
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			if health.Add(1) == 1 {
				io.WriteString(w, `{"api_version":"v1","capabilities":["health","status","snapshot-staging"]}`)
			} else {
				io.WriteString(w, `{"api_version":"v1","capabilities":["health","status"]}`)
			}
		case "/v1/status":
			io.WriteString(w, `{"stack":"stack","engine":"docker","containers":[]}`)
		case "/v1/snapshots":
			uploads.Add(1)
		}
	})}
	defer srv.Close()
	go srv.Serve(l)
	s, _ := safetySender(t, l.Addr().String(), time.Minute)
	id, token := approved(t, s)
	if _, err = s.Stage(context.Background(), "session", id, token); !errors.Is(err, ErrUnsupported) || health.Load() != 2 || uploads.Load() != 0 {
		t.Fatalf("withdrawn capability not enforced: %v", err)
	}
}

func TestStatusMismatchPreventsUpload(t *testing.T) {
	l, e := net.Listen("unix", filepath.Join(t.TempDir(), "s"))
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	var uploads atomic.Int32
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			io.WriteString(w, `{"api_version":"v1","capabilities":["health","status","snapshot-staging"]}`)
		case "/v1/status":
			io.WriteString(w, `{"stack":"wrong","engine":"docker","containers":[]}`)
		case "/v1/snapshots":
			uploads.Add(1)
		}
	})}
	defer srv.Close()
	go srv.Serve(l)
	s, _ := safetySender(t, l.Addr().String(), time.Minute)
	id, token := approved(t, s)
	if _, e = s.Stage(context.Background(), "session", id, token); !errors.Is(e, ErrInvalid) || uploads.Load() != 0 {
		t.Fatal("status mismatch uploaded")
	}
}
func TestCancellationAfterSubmissionUnknown(t *testing.T) {
	l, e := net.Listen("unix", filepath.Join(t.TempDir(), "s"))
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	entered := make(chan struct{})
	release := make(chan struct{})
	var uploads atomic.Int32
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			io.WriteString(w, `{"api_version":"v1","capabilities":["health","status","snapshot-staging"]}`)
		case "/v1/status":
			io.WriteString(w, `{"stack":"stack","engine":"docker","containers":[]}`)
		case "/v1/snapshots":
			uploads.Add(1)
			io.Copy(io.Discard, r.Body)
			close(entered)
			<-release
		}
	})}
	defer srv.Close()
	go srv.Serve(l)
	s, _ := safetySender(t, l.Addr().String(), time.Minute)
	id, token := approved(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, e := s.Stage(ctx, "session", id, token); result <- e }()
	<-entered
	cancel()
	e = <-result
	close(release)
	if !errors.Is(e, dillserve.ErrStageUnknown) || uploads.Load() != 1 {
		t.Fatalf("expected unknown: %v", e)
	}
	if _, e = s.Stage(context.Background(), "session", id, token); !errors.Is(e, ErrInvalid) || uploads.Load() != 1 {
		t.Fatal("cancelled approval replayed")
	}
}
func TestReceiverBoundariesBeforeReview(t *testing.T) {
	s, _ := safetySender(t, "/tmp/not-a-real-socket", time.Minute)
	at := safetySnapshot(make([]byte, 4<<20))
	if _, e := s.Create(context.Background(), "session", at); e != nil {
		t.Fatalf("file at limit: %v", e)
	}
	over := safetySnapshot(make([]byte, (4<<20)+1))
	if _, e := s.Create(context.Background(), "session", over); !errors.Is(e, ErrInvalid) {
		t.Fatal("oversize file accepted")
	}
	files := make([]projectsnapshot.File, 1001)
	for i := range files {
		files[i] = projectsnapshot.File{Path: fmt.Sprintf("%04d", i), SHA256: sha256.Sum256(nil)}
	}
	snap := projectsnapshot.Snapshot{Entrypoint: "0000", Files: files[:1000]}
	if _, e := s.Create(context.Background(), "session", snap); e != nil {
		t.Fatalf("files at limit: %v", e)
	}
	snap.Files = files
	if _, e := s.Create(context.Background(), "session", snap); !errors.Is(e, ErrInvalid) {
		t.Fatal("excess files accepted")
	}
	name := strings.Repeat("a", 255)
	p := name + "/" + strings.Repeat("b", 255) + "/x"
	if len(p) != 513 {
		t.Fatal("bad fixture")
	}
	snap = projectsnapshot.Snapshot{Entrypoint: p, Files: []projectsnapshot.File{{Path: p, SHA256: sha256.Sum256(nil)}}}
	if _, e := s.Create(context.Background(), "session", snap); !errors.Is(e, ErrInvalid) {
		t.Fatal("excess path accepted")
	}
}
