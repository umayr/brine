//go:build darwin || linux || freebsd || openbsd || netbsd

package dillserve

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// SSHOptions identifies a remote SSH host and an explicitly provisioned,
// noninteractive private key. All files and their parent directories must be
// protected from untrusted modification for the tunnel's lifetime.
type SSHOptions struct {
	User, Host, KnownHosts, IdentityFile, RemoteSocket, Stack, Engine string
	Port                                                              uint16
}

// SSHTunnel owns an SSH process and a private local forwarding socket.
// SSH authenticates the host, not the service or its engine.
type SSHTunnel struct {
	client   *Client
	process  sshProcess
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

type sshProcess interface {
	Kill() error
	Wait() error
}
type sshStarter func([]string) (sshProcess, error)
type execSSH struct{ cmd *exec.Cmd }

func (p execSSH) Kill() error { return p.cmd.Process.Kill() }
func (p execSSH) Wait() error { return p.cmd.Wait() }
func startSSH(args []string) (sshProcess, error) {
	// Absolute executable, no shell, no inherited environment, no interactive IO.
	cmd := exec.Command("/usr/bin/ssh", args...)
	cmd.Env = []string{}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return execSSH{cmd}, nil
}

// NewSSH starts a supervised tunnel. ctx controls both startup and lifetime;
// cancelling it requests termination of SSH. Readiness has a ten-second deadline;
// failed startup may wait indefinitely for process termination. Startup only
// succeeds after a v1 health response passes through the forward.
// No interactive authentication is supported. No local fallback is attempted.
func NewSSH(ctx context.Context, o SSHOptions) (*SSHTunnel, error) {
	return newSSH(ctx, o, startSSH, 10*time.Second)
}

func newSSH(ctx context.Context, o SSHOptions, start sshStarter, timeout time.Duration) (*SSHTunnel, error) {
	return newSSHWithPin(ctx, o, "", start, timeout)
}

func newSSHWithPin(ctx context.Context, o SSHOptions, pinEntry string, start sshStarter, timeout time.Duration) (*SSHTunnel, error) {
	if ctx == nil || ctx.Err() != nil || o.Stack == "" || o.Engine == "" || timeout <= 0 {
		return nil, ErrUnavailable
	}
	// /tmp is intentionally fixed and short for sockaddr_un limits. MkdirTemp
	// creates a 0700 directory; same-UID processes remain within the trust boundary.
	dir, err := os.MkdirTemp("/tmp", "dillssh-")
	if err != nil {
		return nil, ErrUnavailable
	}
	socket := filepath.Join(dir, "s")
	known := filepath.Join(dir, "known_hosts")
	cleanup := func() { _ = os.Remove(socket); _ = os.Remove(known); _ = os.Remove(dir) }
	if pinEntry != "" {
		if err := os.WriteFile(known, []byte(pinEntry), 0600); err != nil {
			cleanup()
			return nil, ErrUnavailable
		}
		o.KnownHosts = known
	}
	cfg := sshConfig{User: o.User, Host: o.Host, Port: o.Port, KnownHosts: o.KnownHosts, IdentityFile: o.IdentityFile, RemoteSocket: o.RemoteSocket}
	args, err := sshArgs(cfg, socket)
	if err != nil {
		cleanup()
		return nil, ErrUnavailable
	}
	if pinEntry != "" {
		// -F /dev/null disables configuration; this also excludes certificate algorithms.
		args = append([]string{"-o", "HostKeyAlgorithms=ssh-ed25519"}, args...)
	}
	client, err := NewLocal(Target{Socket: socket, Stack: o.Stack, Engine: o.Engine})
	if err != nil {
		cleanup()
		return nil, ErrUnavailable
	}
	proc, err := start(args)
	if err != nil {
		client.Close()
		cleanup()
		return nil, ErrUnavailable
	}
	life, cancel := context.WithCancel(ctx)
	t := &SSHTunnel{client: client, process: proc, ctx: life, cancel: cancel, done: make(chan struct{})}
	go func() {
		_ = proc.Wait()
		cancel()
		client.Close()
		cleanup()
		close(t.done)
	}()
	go func() { <-life.Done(); t.stop() }()
	startup, stopStartup := context.WithTimeout(life, timeout)
	defer stopStartup()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		// Avoid a long HTTP attempt before the socket exists.
		if fi, e := os.Lstat(socket); e == nil && fi.Mode()&os.ModeSocket != 0 {
			if _, e = client.Health(startup); e == nil && life.Err() == nil && startup.Err() == nil {
				return t, nil
			}
		}
		select {
		case <-startup.Done():
			t.Close()
			return nil, ErrUnavailable
		case <-t.done:
			t.Close()
			return nil, ErrUnavailable
		case <-ticker.C:
		}
	}
}

func (t *SSHTunnel) stop() { t.stopOnce.Do(func() { _ = t.process.Kill() }) }

// Close requests SSH termination and waits for process exit and attempted
// cleanup. Kill and removal errors are ignored; the wait is not bounded.
// It is safe to call concurrently and repeatedly.
func (t *SSHTunnel) Close() { t.cancel(); t.stop(); <-t.done }

func (t *SSHTunnel) requestContext(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil || t.ctx.Err() != nil {
		return nil, nil, ErrUnavailable
	}
	c, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(t.ctx, cancel)
	return c, func() { stop(); cancel() }, nil
}
func (t *SSHTunnel) Health(ctx context.Context) (Health, error) {
	c, done, err := t.requestContext(ctx)
	if err != nil {
		return Health{}, err
	}
	defer done()
	h, err := t.client.Health(c)
	if err != nil || t.ctx.Err() != nil {
		return Health{}, ErrUnavailable
	}
	return h, nil
}

// Stage submits once. A result becomes final only when the lifetime check after
// response processing observes a live tunnel. If the tunnel has ended by then,
// the outcome is unknown, even if a complete response was parsed. An already
// canceled context or invalid body is rejected before any submission.
func (t *SSHTunnel) Stage(ctx context.Context, body []byte) (string, error) {
	if ctx == nil || ctx.Err() != nil || len(body) == 0 || len(body) > 24<<20 {
		return "", ErrUnavailable
	}
	c, done, err := t.requestContext(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	id, err := t.client.Stage(c, body)
	if t.ctx.Err() != nil {
		return "", ErrStageUnknown
	}
	return id, err
}

// Snapshot uses the existing pinned tunnel and is cancelled with its lifetime.
// The caller must hold the configuration lease for the whole call.
func (t *SSHTunnel) Snapshot(ctx context.Context, stageID string) (Manifest, error) {
	c, done, err := t.requestContext(ctx)
	if err != nil {
		return Manifest{}, ErrUnavailable
	}
	defer done()
	m, err := t.client.Snapshot(c, stageID)
	if err != nil || t.ctx.Err() != nil || c.Err() != nil {
		return Manifest{}, ErrUnavailable
	}
	return m, nil
}

func (t *SSHTunnel) Status(ctx context.Context) (Status, error) {
	c, done, err := t.requestContext(ctx)
	if err != nil {
		return Status{}, err
	}
	defer done()
	s, err := t.client.Status(c)
	if err != nil || t.ctx.Err() != nil {
		return Status{}, ErrUnavailable
	}
	return s, nil
}
