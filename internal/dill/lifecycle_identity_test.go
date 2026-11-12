package dill

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLifecycleUsesTrustedStackAndConfiguredEndpoint(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "dill")
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\ncase \" $* \" in *\" -p alias --engine docker --engine-socket /synthetic/target.sock \"*) exit 0;; esac\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	r := New(bin)
	r.Engine = "docker"
	r.Socket = "/synthetic/target.sock"
	r.ResolveStack = func(string) (string, error) { return "alias", nil }
	ctx := context.Background()
	var out bytes.Buffer
	for _, f := range []func() error{func() error { return r.Up(ctx, "/workspace/alias.pkl", &out) }, func() error { return r.Start(ctx, "/workspace/alias.pkl", &out) }, func() error { return r.Restart(ctx, "/workspace/alias.pkl", &out) }, func() error { return r.Stop(ctx, "/workspace/alias.pkl", &out) }, func() error { return r.Down(ctx, "/workspace/alias.pkl", &out) }, func() error { return r.Teardown(ctx, "/workspace/alias.pkl", false, &out) }} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if !strings.Contains(line, "-p alias --engine docker --engine-socket /synthetic/target.sock") || strings.Contains(line, "-p demo") {
			t.Fatalf("unsafe lifecycle args: %s", line)
		}
	}
}
func TestLifecycleEndpointHelper(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		return
	}
	args := os.Args[separator+1:]
	socket, stack := "", ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--engine-socket" {
			socket = args[i+1]
		}
		if args[i] == "-p" {
			stack = args[i+1]
		}
	}
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		os.Exit(2)
	}
	defer conn.Close()
	if _, err = io.WriteString(conn, stack); err != nil {
		os.Exit(2)
	}
}

func TestLifecycleNeverMutatesOtherEndpoint(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "brine-ep-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	paths := []string{filepath.Join(dir, "first.sock"), filepath.Join(dir, "second.sock")}
	calls := make(chan string, 2)
	for i, path := range paths {
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		go func() {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			defer conn.Close()
			data, _ := io.ReadAll(conn)
			calls <- string(rune('0'+i)) + ":" + string(data)
		}()
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "dill")
	if err = os.WriteFile(script, []byte("#!/bin/sh\nexec '"+testBinary+"' -test.run=TestLifecycleEndpointHelper -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r := New(script)
	r.Engine = "docker"
	r.Socket = paths[1]
	r.ResolveStack = func(string) (string, error) { return "alias", nil }
	var out bytes.Buffer
	if err = r.Down(context.Background(), "/workspace/compose.pkl", &out); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-calls:
		if got != "1:alias" {
			t.Fatalf("wrong stack: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("selected endpoint not reached")
	}
	select {
	case <-calls:
		t.Fatal("other endpoint reached")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestUnconfiguredLifecycleFailsClosed(t *testing.T) {
	r := New("unused")
	var out bytes.Buffer
	if err := r.Down(context.Background(), "/synthetic/config", &out); err == nil {
		t.Fatal("unconfigured endpoint accepted")
	}
}

func TestLifecycleDoesNotFallbackWhenEndpointUnsupported(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "old-dill")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r := New(bin)
	r.Engine = "docker"
	r.Socket = "/synthetic/target.sock"
	r.ResolveStack = func(string) (string, error) { return "demo", nil }
	var out bytes.Buffer
	if err := r.Down(context.Background(), "/synthetic/config", &out); err == nil || !strings.Contains(err.Error(), "endpoint-pinning") {
		t.Fatalf("cleanup did not report incompatibility: %v", err)
	}
}
