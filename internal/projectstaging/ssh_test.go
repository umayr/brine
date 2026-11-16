package projectstaging

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/projectreview"
)

func sshFixture(t *testing.T) (*Sender, *projectreview.Current, dillserve.PinnedSSHOptions) {
	t.Helper()
	raw := make([]byte, 51)
	binary.BigEndian.PutUint32(raw, 11)
	copy(raw[4:], "ssh-ed25519")
	binary.BigEndian.PutUint32(raw[15:], 32)
	hash := sha256.Sum256(raw)
	o := dillserve.PinnedSSHOptions{SSHOptions: dillserve.SSHOptions{Host: "host.example", User: "user", Port: 22, IdentityFile: "/tmp/key", RemoteSocket: "/run/dill.sock", Stack: "stack", Engine: "docker"}, PublicKey: "ssh-ed25519 " + base64.StdEncoding.EncodeToString(raw), Fingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(hash[:])}
	c := &projectreview.Current{Stack: o.Stack, Target: projectreview.Target{Kind: projectreview.SSH, Identity: "svc", Socket: o.RemoteSocket, Endpoint: o.Host, SSHHost: o.Host, SSHUser: o.User, SSHPort: o.Port, SSHIdentityFile: o.IdentityFile, SSHPublicKey: o.PublicKey, SSHKnownHost: o.Fingerprint}, Options: map[string]string{"engine": o.Engine}, Variables: map[string]string{"a": "b"}}
	store, e := projectreview.New(20, 1<<20, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(store.Close)
	s, e := NewPinnedSSH(store, "svc", o, func(context.Context) (projectreview.Current, error) { return *c, nil })
	if e != nil {
		t.Fatal(e)
	}
	return s, c, o
}
func TestSSHBindingAndObservedDrift(t *testing.T) {
	mutations := map[string]func(*projectreview.Current){
		"host": func(c *projectreview.Current) { c.Target.SSHHost = "other" }, "endpoint": func(c *projectreview.Current) { c.Target.Endpoint = "other" }, "user": func(c *projectreview.Current) { c.Target.SSHUser = "other" }, "port": func(c *projectreview.Current) { c.Target.SSHPort++ }, "socket": func(c *projectreview.Current) { c.Target.Socket = "/other" }, "identity": func(c *projectreview.Current) { c.Target.Identity = "other" }, "identity-file": func(c *projectreview.Current) { c.Target.SSHIdentityFile = "/other" }, "key": func(c *projectreview.Current) { c.Target.SSHPublicKey = "other" }, "pin": func(c *projectreview.Current) { c.Target.SSHKnownHost = "other" }, "stack": func(c *projectreview.Current) { c.Stack = "other" }, "engine": func(c *projectreview.Current) { c.Options = map[string]string{"engine": "other"} }, "variables": func(c *projectreview.Current) { c.Variables = map[string]string{"a": "other"} }, "local": func(c *projectreview.Current) { c.Target.Kind = projectreview.Local },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s, c, _ := sshFixture(t)
			before := *c
			p, e := s.Create(context.Background(), "s", safetySnapshot([]byte("x")))
			if e != nil {
				t.Fatal(e)
			}
			mutate(c)
			if _, e = s.Confirm(context.Background(), "s", p.ID); e == nil {
				t.Fatal("drift accepted")
			}
			*c = before
			if _, e = s.Confirm(context.Background(), "s", p.ID); e == nil {
				t.Fatal("ABA accepted")
			}
			id, token := approved(t, s)
			mutate(c)
			if _, e = s.Stage(context.Background(), "session", id, token); e == nil {
				t.Fatal("drift accepted")
			}
			*c = before
			if _, e = s.Stage(context.Background(), "session", id, token); e == nil {
				t.Fatal("ABA accepted")
			}
		})
	}
}
func TestSSHWrongPin(t *testing.T) {
	s, _, o := sshFixture(t)
	o.Fingerprint = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, e := NewPinnedSSH(s.store, "svc", o, s.resolve); e == nil {
		t.Fatal("wrong pin accepted")
	}
}
func TestSSHSingleUseCapturedAndUnknown(t *testing.T) {
	s, _, o := sshFixture(t)
	var opens, uploads atomic.Int32
	sock := filepath.Join(t.TempDir(), "s")
	l, e := net.Listen("unix", sock)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			io.WriteString(w, `{"api_version":"v1","capabilities":["health","status","snapshot-staging"]}`)
		case "/v1/status":
			io.WriteString(w, `{"stack":"stack","engine":"docker","containers":[]}`)
		case "/v1/snapshots":
			uploads.Add(1)
			b, _ := io.ReadAll(r.Body)
			if len(b) == 0 {
				t.Error("empty upload")
			}
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
		}
	})}
	defer srv.Close()
	go srv.Serve(l)
	s.openSSH = func(ctx context.Context, got dillserve.PinnedSSHOptions) (stagingClient, error) {
		if got != o {
			t.Error("transport mismatch")
		}
		opens.Add(1)
		return dillserve.NewLocal(dillserve.Target{Socket: sock, Stack: o.Stack, Engine: o.Engine})
	}
	p, e := s.Create(context.Background(), "s", safetySnapshot([]byte("captured")))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Stage(context.Background(), "s", p.ID, ""); e == nil || opens.Load() != 0 {
		t.Fatal("opened before consent")
	}
	token, e := s.Confirm(context.Background(), "s", p.ID)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	var unknown atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.Stage(context.Background(), "s", p.ID, token)
			if errors.Is(e, dillserve.ErrStageUnknown) {
				unknown.Add(1)
			}
		}()
	}
	wg.Wait()
	if opens.Load() != 1 || uploads.Load() != 1 || unknown.Load() != 1 {
		t.Fatalf("opens %d uploads %d unknown %d", opens.Load(), uploads.Load(), unknown.Load())
	}
}
