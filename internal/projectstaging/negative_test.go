package projectstaging

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/projectreview"
	"github.com/umayr/brine/internal/projectsnapshot"
)

func TestMissingCapabilityNeverUploads(t *testing.T) {
	l, e := net.Listen("unix", filepath.Join(t.TempDir(), "s"))
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	var uploads atomic.Int32
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/snapshots":
			uploads.Add(1)
		case "/v1/status":
			io.WriteString(w, `{"stack":"s","engine":"e","containers":[]}`)
		case "/v1/health":
			io.WriteString(w, `{"api_version":"v1","capabilities":["health","status"]}`)
		}
	})}
	defer srv.Close()
	go srv.Serve(l)
	st, _ := projectreview.New(2, 1<<20, time.Minute)
	defer st.Close()
	c := projectreview.Current{Stack: "s", Target: projectreview.Target{Kind: projectreview.Local, Identity: "i", Socket: l.Addr().String()}, Options: map[string]string{"engine": "e"}}
	s, _ := New(st, dillserve.Target{Socket: c.Target.Socket, Stack: "s", Engine: "e"}, "i", func(context.Context) (projectreview.Current, error) { return c, nil })
	b := []byte("x")
	snap := projectsnapshot.Snapshot{Entrypoint: "x", Files: []projectsnapshot.File{{Path: "x", Content: b, SHA256: sha256.Sum256(b)}}}
	p, e := s.Create(context.Background(), "s", snap)
	if e != nil {
		t.Fatal(e)
	}
	token, e := s.Confirm(context.Background(), "s", p.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Stage(context.Background(), "s", p.ID, token); !errors.Is(e, ErrUnsupported) || uploads.Load() != 0 {
		t.Fatal("capability not enforced")
	}
	if _, e = s.Stage(context.Background(), "s", p.ID, token); e == nil {
		t.Fatal("consent reused")
	}
}
