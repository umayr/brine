package agenttransport

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func makeKey(t *testing.T) (ssh.Signer, []byte) {
	t.Helper()
	_, k, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	s, e := ssh.NewSignerFromKey(k)
	if e != nil {
		t.Fatal(e)
	}
	b, e := x509.MarshalPKCS8PrivateKey(k)
	if e != nil {
		t.Fatal(e)
	}
	return s, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b})
}
func TestPinnedForward(t *testing.T) {
	host, _ := makeKey(t)
	other, _ := makeKey(t)
	user, key := makeKey(t)
	dir, err := os.MkdirTemp("/tmp", "at-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	kp := filepath.Join(dir, "key")
	if e := os.WriteFile(kp, key, 0600); e != nil {
		t.Fatal(e)
	}
	remote := filepath.Join(dir, "remote.sock")
	ul, e := net.Listen("unix", remote)
	if e != nil {
		t.Fatal(e)
	}
	defer ul.Close()
	go func() {
		for {
			c, e := ul.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	var auths, opens atomic.Int32
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		auths.Add(1)
		if string(k.Marshal()) != string(user.PublicKey().Marshal()) {
			return nil, io.EOF
		}
		return nil, nil
	}}
	cfg.AddHostKey(host)
	// Real OpenSSH hosts commonly offer ECDSA as well as the enrolled Ed25519
	// key. The client's default algorithm preference must not override the pin.
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecdsaSigner, err := ssh.NewSignerFromKey(ecdsaKey)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AddHostKey(ecdsaSigner)
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			go func() {
				s, ch, r, e := ssh.NewServerConn(c, cfg)
				if e != nil {
					return
				}
				defer s.Close()
				go ssh.DiscardRequests(r)
				for v := range ch {
					if v.ChannelType() != "direct-streamlocal@openssh.com" {
						_ = v.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					var p struct {
						Path      string
						Reserved  string
						Reserved2 uint32
					}
					if ssh.Unmarshal(v.ExtraData(), &p) != nil || p.Path != remote {
						_ = v.Reject(ssh.Prohibited, "bad path")
						continue
					}
					opens.Add(1)
					dst, e := net.Dial("unix", p.Path)
					if e != nil {
						_ = v.Reject(ssh.ConnectionFailed, "failed")
						continue
					}
					channel, req, e := v.Accept()
					if e != nil {
						dst.Close()
						continue
					}
					go ssh.DiscardRequests(req)
					go func() {
						defer dst.Close()
						defer channel.Close()
						go io.Copy(dst, channel)
						_, _ = io.Copy(channel, dst)
					}()
				}
			}()
		}
	}()
	base := Config{Address: ln.Addr().String(), User: "test", KeyPath: kp, HostPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(host.PublicKey()))), RemoteSocket: remote, LocalDir: dir, Timeout: time.Second * 3}
	bad := base
	bad.HostPublicKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(other.PublicKey())))
	if f, e := Open(context.Background(), bad); e == nil {
		f.Close()
		t.Fatal("mismatch accepted")
	}
	if auths.Load() != 0 {
		t.Fatal("authentication before host verification")
	}
	ctx, cancel := context.WithCancel(context.Background())
	f, e := Open(ctx, base)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	st, e := os.Stat(f.Socket())
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions: %v %v", st, e)
	}
	c, e := net.Dial("unix", f.Socket())
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	if _, e = c.Write([]byte("hello")); e != nil {
		t.Fatal(e)
	}
	b := make([]byte, 5)
	if _, e = io.ReadFull(c, b); e != nil || string(b) != "hello" {
		t.Fatalf("echo %q %v", b, e)
	}
	cancel()
	select {
	case <-f.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("cancel not propagated")
	}
	if _, e = c.Read(b); e == nil {
		t.Fatal("read not interrupted")
	}
	if opens.Load() != 1 {
		t.Fatalf("opens=%d", opens.Load())
	}
}
func TestRejectUnsafeInputs(t *testing.T) {
	_, k := makeKey(t)
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	p := filepath.Join(dir, "key")
	_ = os.WriteFile(p, k, 0644)
	if _, e := signer(p); e == nil {
		t.Fatal("accepted public key permissions")
	}
	if _, e := pinned("not a key"); e == nil {
		t.Fatal("accepted bad pin")
	}
	if e := os.Symlink(p, filepath.Join(dir, "link")); e != nil {
		t.Fatal(e)
	}
	if _, e := signer(filepath.Join(dir, "link")); e == nil {
		t.Fatal("accepted symlink")
	}
}
