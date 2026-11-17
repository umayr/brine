//go:build linux || darwin

package revealsigner

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/umayr/brine/internal/remotereveal"
	"golang.org/x/sys/unix"
)

var _ remotereveal.Signer = (*Signer)(nil)

const seedHex = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
const signatureHex = "e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e06522490155" + "5fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b"

func TestOpenAndSign(t *testing.T) {
	for _, suffix := range []string{"", "\n"} {
		t.Run(string(rune('0'+len(suffix))), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "key")
			if err := os.WriteFile(path, []byte(seedHex+suffix), 0600); err != nil {
				t.Fatal(err)
			}
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			sig, err := s.Sign(context.Background(), nil)
			if err != nil || hex.EncodeToString(sig) != signatureHex {
				t.Fatalf("wrong signature: %v", err)
			}
			pub := s.PublicKey()
			expected := append(ed25519.PublicKey(nil), pub...)
			pub[0] ^= 0xff
			if string(s.PublicKey()) != string(expected) {
				t.Fatal("public key aliases signer")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if sig, err := s.Sign(ctx, nil); !errors.Is(err, context.Canceled) || sig != nil {
				t.Fatal("cancellation ignored")
			}
		})
	}
}

func TestConcurrentSignAndPublicKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(seedHex), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				pub := s.PublicKey()
				sig, err := s.Sign(context.Background(), nil)
				if err != nil || !ed25519.Verify(pub, nil, sig) {
					t.Error("invalid concurrent signature")
					return
				}
				pub[0] ^= 0xff
			}
		}()
	}
	wg.Wait()
}

func TestRejectFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrKey) {
		t.Fatal("FIFO accepted")
	}
}

func TestRejectUnsafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	invalid := []string{"", seedHex[:63], strings.ToUpper(seedHex), seedHex + "\n\n", seedHex + "x", strings.Repeat("a", 10000), strings.Repeat("z", 64)}
	if _, err := Open(path); !errors.Is(err, ErrKey) {
		t.Fatal("missing accepted")
	}
	for _, content := range invalid {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Open(path)
		if !errors.Is(err, ErrKey) {
			t.Fatal("invalid key accepted")
		}
		if strings.Contains(err.Error(), content) && content != "" {
			t.Fatal("content leaked")
		}
	}
	if err := os.WriteFile(path, []byte(seedHex), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0400, 0640, 0601, 0700, os.ModeSetuid | 0600} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path); !errors.Is(err, ErrKey) {
			t.Fatalf("mode %o accepted", mode)
		}
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); !errors.Is(err, ErrKey) {
		t.Fatal("symlink accepted")
	}
	for _, p := range []string{"key", "/", path + "/../key", dir} {
		if _, err := Open(p); !errors.Is(err, ErrKey) {
			t.Fatalf("path accepted: %q", p)
		}
	}
	// Ownership changes generally require privilege; exercise when available.
	if os.Geteuid() == 0 {
		if err := os.Chown(path, 1, -1); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path); !errors.Is(err, ErrKey) {
			t.Fatal("foreign owner accepted")
		}
	}
}
