//go:build darwin || linux || freebsd || openbsd || netbsd

package dillserve

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSSH struct {
	once     sync.Once
	done     chan struct{}
	server   *http.Server
	listener net.Listener
}

func (f *fakeSSH) Kill() error {
	f.once.Do(func() {
		if f.server != nil {
			_ = f.server.Close()
		}
		if f.listener != nil {
			_ = f.listener.Close()
		}
		close(f.done)
	})
	return nil
}
func (f *fakeSSH) Wait() error { <-f.done; return errors.New("fake exited") }
func options(t *testing.T) SSHOptions {
	t.Helper()
	file := filepath.Join(t.TempDir(), "keys")
	if err := os.WriteFile(file, []byte("key"), 0600); err != nil {
		t.Fatal(err)
	}
	return SSHOptions{User: "operator", Host: "example.test", Port: 22, KnownHosts: file, IdentityFile: file, RemoteSocket: "/run/service.sock", Stack: "demo", Engine: "docker"}
}
func fakeListener(t *testing.T, health string, seen chan<- string, process chan<- *fakeSSH) sshStarter {
	return func(args []string) (sshProcess, error) {
		var socket string
		for i, a := range args {
			if a == "-L" && i+1 < len(args) {
				socket = strings.Split(args[i+1], ":")[0]
			}
		}
		if socket == "" {
			t.Error("missing forward")
			return nil, errors.New("missing")
		}
		fi, err := os.Stat(filepath.Dir(socket))
		if err != nil || fi.Mode().Perm() != 0700 {
			t.Errorf("private dir: %v %v", fi, err)
		}
		l, err := net.Listen("unix", socket)
		if err != nil {
			return nil, err
		}
		s := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/health" {
				_, _ = w.Write([]byte(health))
			} else {
				_, _ = w.Write([]byte(goodStatus))
			}
		})}
		f := &fakeSSH{done: make(chan struct{}), server: s, listener: l}
		go func() { _ = s.Serve(l) }()
		seen <- socket
		process <- f
		return f, nil
	}
}
func TestSSHTunnelLifecycle(t *testing.T) {
	seen := make(chan string, 2)
	processes := make(chan *fakeSSH, 2)
	o := options(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := newSSH(ctx, o, fakeListener(t, goodHealth, seen, processes), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	path := <-seen
	p := <-processes
	b, err := newSSH(context.Background(), o, fakeListener(t, goodHealth, seen, processes), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	path2 := <-seen
	<-processes
	if path == path2 {
		t.Fatal("socket reused")
	}
	if _, err := a.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = p.Kill()
	<-a.done
	if _, err := a.Health(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("exit: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); a.Close() }()
	}
	wg.Wait()
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("directory remains: %v", err)
	}
	if _, err := b.Health(context.Background()); err != nil {
		t.Fatalf("other tunnel harmed: %v", err)
	}
	b.Close()
	if _, err := os.Stat(filepath.Dir(path2)); !os.IsNotExist(err) {
		t.Fatalf("directory remains: %v", err)
	}
}
func TestSSHStartupFailures(t *testing.T) {
	o := options(t)
	t.Run("start error", func(t *testing.T) {
		var dir string
		_, err := newSSH(context.Background(), o, func(args []string) (sshProcess, error) {
			for i, a := range args {
				if a == "-L" {
					dir = filepath.Dir(strings.Split(args[i+1], ":")[0])
				}
			}
			return nil, errors.New("fail")
		}, time.Second)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
		if _, e := os.Stat(dir); !os.IsNotExist(e) {
			t.Fatal(e)
		}
	})
	t.Run("process exit", func(t *testing.T) {
		_, err := newSSH(context.Background(), o, func([]string) (sshProcess, error) {
			p := &fakeSSH{done: make(chan struct{})}
			_ = p.Kill()
			return p, nil
		}, time.Second)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
	})
	t.Run("health timeout", func(t *testing.T) {
		seen := make(chan string, 1)
		ps := make(chan *fakeSSH, 1)
		start := time.Now()
		_, err := newSSH(context.Background(), o, fakeListener(t, `{}`, seen, ps), 80*time.Millisecond)
		path := <-seen
		if !errors.Is(err, ErrUnavailable) || time.Since(start) > time.Second {
			t.Fatalf("%v", err)
		}
		if _, e := os.Stat(filepath.Dir(path)); !os.IsNotExist(e) {
			t.Fatal(e)
		}
	})
	t.Run("cancel startup", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		_, err := newSSH(ctx, o, func([]string) (sshProcess, error) { p := &fakeSSH{done: make(chan struct{})}; cancel(); return p, nil }, time.Second)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
	})
}
func TestSSHLifetimeCancellation(t *testing.T) {
	seen := make(chan string, 1)
	ps := make(chan *fakeSSH, 1)
	ctx, cancel := context.WithCancel(context.Background())
	tunnel, err := newSSH(ctx, options(t), fakeListener(t, goodHealth, seen, ps), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	path := <-seen
	<-ps
	cancel()
	select {
	case <-tunnel.done:
	case <-time.After(time.Second):
		t.Fatal("not stopped")
	}
	if _, err := tunnel.Status(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	tunnel.Close()
}
