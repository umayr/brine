package dillserve

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSSHArgs(t *testing.T) {
	known := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(known, []byte("example ssh-ed25519 AAAA\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c := sshConfig{User: "operator", Host: "example.test", Port: 22, KnownHosts: known, IdentityFile: known, RemoteSocket: "/run/dill.sock"}
	args, err := sshArgs(c, "/tmp/private/sock")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-F", "/dev/null", "-N", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + known, "-o", "GlobalKnownHostsFile=/dev/null", "-o", "UpdateHostKeys=no", "-o", "VerifyHostKeyDNS=no", "-o", "CheckHostIP=no", "-o", "ProxyCommand=none", "-o", "ProxyJump=none", "-o", "PermitLocalCommand=no", "-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "ExitOnForwardFailure=yes", "-o", "StreamLocalBindUnlink=no", "-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "IdentityAgent=none", "-o", "IdentitiesOnly=yes", "-o", "PreferredAuthentications=publickey", "-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no", "-o", "GSSAPIAuthentication=no", "-o", "HostbasedAuthentication=no", "-o", "Tunnel=no", "-o", "RemoteCommand=none", "-i", known, "-l", "operator", "-p", "22", "-L", "/tmp/private/sock:/run/dill.sock", "example.test"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("got %#v", args)
	}
}

func TestSSHArgsRejectUnsafeInputs(t *testing.T) {
	known := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(known, nil, 0600); err != nil {
		t.Fatal(err)
	}
	good := sshConfig{User: "operator", Host: "example.test", Port: 22, KnownHosts: known, IdentityFile: known, RemoteSocket: "/run/dill.sock"}
	tests := []struct {
		name   string
		mutate func(*sshConfig)
		local  string
	}{
		{"user option", func(c *sshConfig) { c.User = "-oProxyCommand=x" }, "/tmp/s"},
		{"host option", func(c *sshConfig) { c.Host = "-bad" }, "/tmp/s"},
		{"host whitespace", func(c *sshConfig) { c.Host = "x -oX=y" }, "/tmp/s"},
		{"missing user", func(c *sshConfig) { c.User = "" }, "/tmp/s"},
		{"missing host", func(c *sshConfig) { c.Host = "" }, "/tmp/s"},
		{"port", func(c *sshConfig) { c.Port = 0 }, "/tmp/s"},
		{"identity absent", func(c *sshConfig) { c.IdentityFile = "" }, "/tmp/s"},
		{"identity missing", func(c *sshConfig) { c.IdentityFile = "/no/such/key" }, "/tmp/s"},
		{"known hosts absent", func(c *sshConfig) { c.KnownHosts = "" }, "/tmp/s"},
		{"known hosts missing", func(c *sshConfig) { c.KnownHosts = "/no/such/known_hosts" }, "/tmp/s"},
		{"known hosts directory", func(c *sshConfig) { c.KnownHosts = filepath.Dir(known) }, "/tmp/s"},
		{"known hosts expansion", func(c *sshConfig) { c.KnownHosts = "/tmp/%h" }, "/tmp/s"},
		{"remote colon", func(c *sshConfig) { c.RemoteSocket = "/run/a:/run/b" }, "/tmp/s"},
		{"remote traversal", func(c *sshConfig) { c.RemoteSocket = "/run/../tmp/s" }, "/tmp/s"},
		{"remote relative", func(c *sshConfig) { c.RemoteSocket = "run/s" }, "/tmp/s"},
		{"remote oversized", func(c *sshConfig) { c.RemoteSocket = "/" + strings.Repeat("a", 90) }, "/tmp/s"},
		{"local colon", func(c *sshConfig) {}, "/tmp/a:b"},
		{"local whitespace", func(c *sshConfig) {}, "/tmp/a b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := good
			tt.mutate(&c)
			if _, err := sshArgs(c, tt.local); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
