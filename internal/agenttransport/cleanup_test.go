package agenttransport

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

var closeFault = errors.New("injected close failure")
var mkdirFault = errors.New("injected mkdir failure")

type failingClose struct{ net.Conn }

func (c failingClose) Close() error { _ = c.Conn.Close(); return closeFault }
func TestSetupCloseFailure(t *testing.T) {
	host, _ := makeKey(t)
	_, key := makeKey(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	kp := filepath.Join(dir, "key")
	if err := os.WriteFile(kp, key, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Address: "fake:22", User: "test", KeyPath: kp, HostPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(host.PublicKey()))), RemoteSocket: "/agent.sock", LocalDir: dir, Timeout: time.Second}
	for _, phase := range []string{"handshake", "cancellation", "mkdir"} {
		t.Run(phase, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			client, err := net.Dial("tcp", listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			server, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			serverDone := make(chan struct{})
			go func() {
				defer close(serverDone)
				if phase == "handshake" {
					_, _ = server.Write([]byte("invalid\r\n"))
					_ = server.Close()
					return
				}
				if phase == "cancellation" {
					cancel()
					return
				}
				sc := &ssh.ServerConfig{NoClientAuth: true}
				sc.AddHostKey(host)
				s, _, _, err := ssh.NewServerConn(server, sc)
				if err == nil {
					defer s.Close()
					<-ctx.Done()
				}
			}()
			mkdirReached := false
			d := setupDeps{dial: func(context.Context, string, string) (net.Conn, error) { return failingClose{client}, nil }, mkdirTemp: func(string, string) (string, error) { mkdirReached = true; return "", mkdirFault }}
			_, err = open(ctx, cfg, d)
			cancel()
			<-serverDone
			if err == nil || !errors.Is(err, ErrCleanup) || !errors.Is(err, closeFault) {
				t.Fatalf("%s: %v", phase, err)
			}
			switch phase {
			case "handshake":
				if !strings.Contains(err.Error(), "SSH handshake:") || mkdirReached {
					t.Fatalf("unexpected handshake phase: %v", err)
				}
			case "cancellation":
				if !errors.Is(err, context.Canceled) || mkdirReached {
					t.Fatalf("unexpected cancellation phase: %v", err)
				}
			case "mkdir":
				if !mkdirReached || !errors.Is(err, mkdirFault) {
					t.Fatalf("mkdir not reached: %v", err)
				}
			}
		})
	}
}
