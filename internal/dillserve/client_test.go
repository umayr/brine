package dillserve

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func server(t *testing.T, health, status string) (string, *atomic.Int32) {
	t.Helper()
	dir, e := os.MkdirTemp("/tmp", "ds-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	l, e := net.Listen("unix", path)
	if e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	s := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/v1/health":
			_, _ = w.Write([]byte(health))
		case "/v1/status":
			_, _ = w.Write([]byte(status))
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = s.Serve(l) }()
	t.Cleanup(func() { _ = s.Close() })
	return path, &calls
}

const goodHealth = `{"api_version":"v1","capabilities":["health","status"]}`
const goodStatus = `{"stack":"demo","engine":"docker","containers":[]}`

func TestStatus(t *testing.T) {
	path, calls := server(t, goodHealth, goodStatus)
	c, e := NewLocal(Target{path, "demo", "docker"})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	s, e := c.Status(context.Background())
	if e != nil || s.Stack != "demo" || calls.Load() != 2 {
		t.Fatalf("status %#v %v calls %d", s, e, calls.Load())
	}
}
func TestFailClosed(t *testing.T) {
	cases := []struct {
		name, health, status string
		calls                int32
	}{
		{"version", `{"api_version":"v2","capabilities":["health","status"]}`, goodStatus, 1},
		{"missing capability", `{"api_version":"v1","capabilities":["health"]}`, goodStatus, 1},
		{"duplicate", `{"api_version":"v1","api_version":"v1","capabilities":["health","status"]}`, goodStatus, 1},
		{"malformed", `{`, goodStatus, 1}, {"oversized", strings.Repeat("x", maxResponse+1), goodStatus, 1},
		{"wrong stack", goodHealth, `{"stack":"other","engine":"docker","containers":[]}`, 2},
		{"wrong engine", goodHealth, `{"stack":"demo","engine":"other","containers":[]}`, 2},
		{"missing containers", goodHealth, `{"stack":"demo","engine":"docker"}`, 2},
		{"null containers", goodHealth, `{"stack":"demo","engine":"docker","containers":null}`, 2},
		{"trailing", goodHealth, goodStatus + ` {}`, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, calls := server(t, tc.health, tc.status)
			c, e := NewLocal(Target{path, "demo", "docker"})
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			_, e = c.Status(context.Background())
			if !errors.Is(e, ErrUnavailable) || calls.Load() != tc.calls {
				t.Fatalf("err %v calls %d", e, calls.Load())
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRequestDeadlineAndCancellation(t *testing.T) {
	c, err := NewLocal(Target{filepath.Join(t.TempDir(), "s.sock"), "demo", "docker"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var paths []string
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		remaining := time.Until(deadline)
		if !ok || remaining <= 10*time.Second || remaining > 15*time.Second {
			t.Errorf("%s deadline remaining %v, present %v", r.URL.Path, remaining, ok)
		}
		paths = append(paths, r.URL.Path)
		body := goodHealth
		if r.URL.Path == "/v1/status" {
			body = goodStatus
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	if _, err := c.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != "/v1/health,/v1/status" {
		t.Fatalf("requests: %v", paths)
	}

	started := make(chan struct{})
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.Health(ctx); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("caller cancellation did not stop request")
	}

	deadline := time.Now().Add(time.Second)
	ctx, cancel = context.WithDeadline(context.Background(), deadline)
	defer cancel()
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got, ok := r.Context().Deadline()
		if !ok || !got.Equal(deadline) {
			t.Errorf("caller deadline: %v, present %v", got, ok)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(goodHealth)), Header: make(http.Header)}, nil
	})
	if _, err := c.Health(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestResponseSizeBoundary(t *testing.T) {
	// Padding is inside an ignored JSON string, with the newline counted on wire.
	prefix := `{"stack":"demo","engine":"docker","containers":[],"padding":"`
	suffix := "\"}\n"
	exact := prefix + strings.Repeat("x", maxResponse-len(prefix)-len(suffix)) + suffix
	cases := []struct {
		name, body string
		valid      bool
	}{
		{"exact including newline", exact, true},
		{"one byte over with newline", exact[:len(exact)-1] + " \n", false},
		{"one byte over valid JSON", exact + " ", false},
		{"exact malformed", exact[:len(exact)-2] + "!\n", false},
		{"exact trailing value", goodStatus + strings.Repeat(" ", maxResponse-len(goodStatus)-2) + "{}", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := server(t, goodHealth, tc.body)
			c, err := NewLocal(Target{path, "demo", "docker"})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_, err = c.Status(context.Background())
			if tc.valid && err != nil || !tc.valid && !errors.Is(err, ErrUnavailable) {
				t.Fatalf("length %d: %v", len(tc.body), err)
			}
		})
	}
}

func TestTransportFailureAndNoFallback(t *testing.T) {
	if _, e := NewLocal(Target{"relative", "demo", "docker"}); e == nil {
		t.Fatal("relative socket accepted")
	}
	c, e := NewLocal(Target{filepath.Join(t.TempDir(), "missing"), "demo", "docker"})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if _, e = c.Status(context.Background()); !errors.Is(e, ErrUnavailable) {
		t.Fatalf("%v", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = c.Health(ctx); !errors.Is(e, ErrUnavailable) {
		t.Fatalf("%v", e)
	}
}
