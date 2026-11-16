//go:build darwin || linux || freebsd || openbsd || netbsd

package dillserve

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testPin() (string, string) {
	raw := make([]byte, 51)
	binary.BigEndian.PutUint32(raw, 11)
	copy(raw[4:], "ssh-ed25519")
	binary.BigEndian.PutUint32(raw[15:], 32)
	for i := 19; i < 51; i++ {
		raw[i] = byte(i)
	}
	sum := sha256.Sum256(raw)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(raw), "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}
func TestPinnedEntry(t *testing.T) {
	key, fp := testPin()
	for _, tc := range []struct {
		host          string
		port          uint16
		key, fp, want string
	}{
		{"example.test", 22, key, fp, "example.test " + key + "\n"},
		{"example.test", 2222, key, fp, "[example.test]:2222 " + key + "\n"},
		// Syntax acceptance does not constrain resolver search, aliases or numeric interpretation.
		{"short", 22, key, fp, "short " + key + "\n"},
		{"0x7f000001", 22, key, fp, "0x7f000001 " + key + "\n"},
		{"127.1", 22, key, fp, ""},
		{"EXAMPLE.test", 22, key, fp, ""}, {"example.test\n*", 22, key, fp, ""}, {"*.example", 22, key, fp, ""}, {"127.0.0.1", 22, key, fp, ""}, {"example.", 22, key, fp, ""}, {"-example", 22, key, fp, ""}, {"example", 0, key, fp, ""},
		{"example", 22, key, fp + "=", ""}, {"example", 22, key + " comment", fp, ""}, {"example", 22, key + "\n", fp, ""}, {"example", 22, "ssh-rsa AAAA", fp, ""}, {"example", 22, "ssh-ed25519 AAAA", fp, ""}, {"example", 22, key, "SHA256:wrong", ""},
	} {
		got, err := pinnedEntry(tc.host, tc.port, tc.key, tc.fp)
		if got != tc.want || (err == nil) != (tc.want != "") {
			t.Errorf("%q %d: got %q %v", tc.host, tc.port, got, err)
		}
	}
}

// RFC 8032 public key; the fingerprint was independently obtained with
// ssh-keygen -lf /dev/stdin -E sha256, without a network connection.
func TestPinnedEntrySSHKeygenVector(t *testing.T) {
	const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINdamAGCsQq31Uv+08lkBzoO4XLz2qYjJa8CGmj3B1Ea"
	const fingerprint = "SHA256:bbXpuKG6zhzdmnxq256TlqzFBzRl2f6OOg722cYNbU8"
	got, err := pinnedEntry("example.test", 22, key, fingerprint)
	if err != nil || got != "example.test "+key+"\n" {
		t.Fatalf("external fingerprint vector: %q %v", got, err)
	}
}

func TestPinnedTunnelPrivateLifetime(t *testing.T) {
	key, fp := testPin()
	base := options(t)
	base.KnownHosts = ""
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	processes := make(chan *fakeSSH, 1)
	seen := make(chan string, 4)
	listener := fakeListener(t, goodHealth, seen, processes)
	var dir, known string
	start := func(args []string) (sshProcess, error) {
		joined := strings.Join(args, " ")
		for _, required := range []string{"HostKeyAlgorithms=ssh-ed25519", "StrictHostKeyChecking=yes", "GlobalKnownHostsFile=/dev/null", "VerifyHostKeyDNS=no", "UpdateHostKeys=no", "-F /dev/null", "ControlPath=none"} {
			if !strings.Contains(joined, required) {
				t.Errorf("missing %s", required)
			}
		}
		for _, arg := range args {
			if strings.HasPrefix(arg, "UserKnownHostsFile=") {
				known = strings.TrimPrefix(arg, "UserKnownHostsFile=")
			}
		}
		dir = filepath.Dir(known)
		want := []string{
			"-o", "HostKeyAlgorithms=ssh-ed25519", "-F", "/dev/null", "-N", "-T",
			"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
			"-o", "UserKnownHostsFile=" + known, "-o", "GlobalKnownHostsFile=/dev/null", "-o", "UpdateHostKeys=no",
			"-o", "VerifyHostKeyDNS=no", "-o", "CheckHostIP=no", "-o", "ProxyCommand=none", "-o", "ProxyJump=none",
			"-o", "PermitLocalCommand=no", "-o", "ForwardAgent=no", "-o", "ForwardX11=no",
			"-o", "ExitOnForwardFailure=yes", "-o", "StreamLocalBindUnlink=no", "-o", "ControlMaster=no", "-o", "ControlPath=none",
			"-o", "IdentityAgent=none", "-o", "IdentitiesOnly=yes", "-o", "PreferredAuthentications=publickey", "-o", "PasswordAuthentication=no",
			"-o", "KbdInteractiveAuthentication=no", "-o", "GSSAPIAuthentication=no", "-o", "HostbasedAuthentication=no", "-o", "Tunnel=no", "-o", "RemoteCommand=none",
			"-i", base.IdentityFile, "-l", base.User, "-p", "22", "-L", filepath.Join(dir, "s") + ":" + base.RemoteSocket, base.Host,
		}
		if !reflect.DeepEqual(args, want) {
			t.Errorf("SSH argv mismatch\n got: %#v\nwant: %#v", args, want)
		}
		data, err := os.ReadFile(known)
		if err != nil || string(data) != "example.test "+key+"\n" {
			t.Errorf("known hosts %q %v", data, err)
		}
		for p, mode := range map[string]os.FileMode{dir: 0700, known: 0600} {
			fi, e := os.Stat(p)
			if e != nil || fi.Mode().Perm() != mode {
				t.Errorf("mode %s: %v %v", p, fi, e)
			}
		}
		return listener(args)
	}
	tunnel, err := newPinnedSSH(ctx, PinnedSSHOptions{SSHOptions: base, PublicKey: key, Fingerprint: fp}, start, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(known); err != nil {
		t.Fatal(err)
	}
	cancel()
	tunnel.Close()
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("directory remains: %v", err)
	}
}
func TestPinnedTunnelRejectAndCleanup(t *testing.T) {
	key, fp := testPin()
	base := options(t)
	base.KnownHosts = ""
	called := false
	start := func(args []string) (sshProcess, error) { called = true; return nil, errors.New("failed") }
	o := PinnedSSHOptions{SSHOptions: base, PublicKey: key, Fingerprint: "SHA256:wrong"}
	if _, err := newPinnedSSH(context.Background(), o, start, time.Second); err == nil || called {
		t.Fatal("mismatch started")
	}
	o.Fingerprint = fp
	o.KnownHosts = "/tmp/ambient"
	if _, err := newPinnedSSH(context.Background(), o, start, time.Second); err == nil || called {
		t.Fatal("ambient file accepted")
	}
	o.KnownHosts = ""
	var dir string
	_, err := newPinnedSSH(context.Background(), o, func(args []string) (sshProcess, error) {
		for _, a := range args {
			if strings.HasPrefix(a, "UserKnownHostsFile=") {
				dir = filepath.Dir(strings.TrimPrefix(a, "UserKnownHostsFile="))
			}
		}
		return nil, errors.New("failed")
	}, time.Second)
	if err == nil || dir == "" {
		t.Fatal("expected startup failure")
	}
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("failed startup leaked: %v", err)
	}
}
