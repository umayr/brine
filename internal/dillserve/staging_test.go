package dillserve

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestStageNilContext(t *testing.T) {
	// A nil context must be rejected before accessing the transport.
	c := &Client{}
	if _, err := c.Stage(nil, []byte(`{}`)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
}

func TestStageResponses(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
		want error
	}{
		{"malformed", 201, `{"status":"staged","stage_id":"ABC"}`, ErrStageUnknown},
		{"duplicate", 201, `{"status":"staged","status":"staged","stage_id":"0123456789abcdef0123456789abcdef"}`, ErrStageUnknown},
		{"oversize", 201, strings.Repeat("x", maxResponse+1), ErrStageUnknown},
		{"server failure", 500, "secret", ErrStageUnknown}, {"rejected", 400, "secret", ErrStageRejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, e := net.Listen("unix", filepath.Join(t.TempDir(), "s"))
			if e != nil {
				t.Fatal(e)
			}
			defer l.Close()
			var calls atomic.Int32
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.code)
				io.WriteString(w, tc.body)
			})}
			defer server.Close()
			go server.Serve(l)
			c, e := NewLocal(Target{Socket: l.Addr().String(), Stack: "s", Engine: "e"})
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			_, e = c.Stage(context.Background(), []byte(`{}`))
			if !errors.Is(e, tc.want) || strings.Contains(e.Error(), "secret") || calls.Load() != 1 {
				t.Fatalf("unexpected outcome %v calls %d", e, calls.Load())
			}
		})
	}
}
func TestStageDisconnectUnknown(t *testing.T) {
	l, e := net.Listen("unix", filepath.Join(t.TempDir(), "s"))
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	var calls atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	})}
	defer server.Close()
	go server.Serve(l)
	c, _ := NewLocal(Target{Socket: l.Addr().String(), Stack: "s", Engine: "e"})
	defer c.Close()
	if _, e = c.Stage(context.Background(), []byte(`{}`)); !errors.Is(e, ErrStageUnknown) || calls.Load() != 1 {
		t.Fatalf("%v calls %d", e, calls.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = c.Stage(ctx, []byte(`{}`)); !errors.Is(e, ErrUnavailable) || calls.Load() != 1 {
		t.Fatal("cancelled request sent")
	}
}
