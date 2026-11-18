package agenttransport

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestPinnedRSAHostNegotiatesSHA2(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaSigner, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := makeKey(t)
	_, userKey := makeKey(t)
	dir, err := os.MkdirTemp("/tmp", "rsa-pin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, userKey, 0600); err != nil {
		t.Fatal(err)
	}
	for _, algorithm := range []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256} {
		t.Run(algorithm, func(t *testing.T) {
			sha2Signer, err := ssh.NewSignerWithAlgorithms(rsaSigner.(ssh.AlgorithmSigner), []string{algorithm})
			if err != nil {
				t.Fatal(err)
			}
			config := &ssh.ServerConfig{NoClientAuth: true}
			config.AddHostKey(other)
			config.AddHostKey(sha2Signer)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				server, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					_ = channel.Reject(ssh.Prohibited, "no channels needed")
				}
			}()
			cfg := Config{Address: listener.Addr().String(), User: "test", KeyPath: keyPath, HostPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(rsaSigner.PublicKey()))), RemoteSocket: "/test.sock", LocalDir: dir, Timeout: 3 * time.Second}
			forward, err := Open(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := forward.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("server did not close")
			}
		})
	}
}
