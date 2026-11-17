// Package agenttransport implements an experimental, pinned SSH streamlocal
// forward for agentclient. It does not enroll or qualify hosts or agents.
package agenttransport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

// ErrCleanup marks failed resource cleanup during transport setup.
var ErrCleanup = errors.New("agenttransport: cleanup failed")

func cleanupError(err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(ErrCleanup, err)
}

// Config contains independently provisioned connection details. KeyPath must
// name a dedicated, unencrypted SSH private key owned by the current user.
// LocalDir must be an existing private directory owned by the current user.
// Its ancestors and the key's ancestors must be trusted against replacement.
// Operators must independently verify ACLs grant no unintended access to the
// key, local directory and ancestors; mode checks do not enforce ACL privacy.
// RemoteSocket must be an absolute, clean Unix socket path on the remote host.
// Timeout bounds TCP connection and SSH handshake, each deferred channel-open,
// and individual blocked proxy writes. A local disconnect while channel-open
// stalls may not be detected until it returns or Timeout expires. An idle
// connection has no timeout.
type Config struct {
	Address, User, KeyPath, HostPublicKey, RemoteSocket, LocalDir string
	Timeout                                                       time.Duration
}

// Forward owns one SSH connection, one streamlocal channel and at most one
// local connection. Context is cancelled on either side's death or Close.
// Pass Context() and Socket() to agentclient.New; close Forward when finished.
type Forward struct {
	ctx      context.Context
	cancel   context.CancelFunc
	once     sync.Once
	closeErr error
	client   *ssh.Client
	conn     *trackedConn
	listener *net.UnixListener
	remote   net.Conn
	local    net.Conn
	dir      string
	mu       sync.Mutex
}

func (f *Forward) Context() context.Context { return f.ctx }
func (f *Forward) Socket() string           { return filepath.Join(f.dir, "agent.sock") }

// Close tears down the forward and returns transport and filesystem cleanup errors.
// Repeated or concurrent calls return the same cleanup result.
func (f *Forward) Close() error {
	f.once.Do(func() {
		f.cancel()
		f.mu.Lock()
		if f.listener != nil {
			_ = f.listener.Close()
		}
		if f.local != nil {
			_ = f.local.Close()
		}
		// Closing the SSH connection first unblocks channel operations even if
		// a channel close packet could not otherwise be written.
		if f.client != nil {
			_ = f.client.Close()
		}
		if f.conn != nil {
			f.closeErr = errors.Join(f.closeErr, f.conn.Close())
		}
		if f.remote != nil {
			_ = f.remote.Close()
		}
		f.mu.Unlock()
		if f.dir != "" {
			for _, p := range []string{f.Socket(), f.dir} {
				if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
					f.closeErr = errors.Join(f.closeErr, fmt.Errorf("remove %s: %w", p, err))
				}
			}
		}
	})
	return f.closeErr
}

func cleanAbs(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && p != "/" && !strings.ContainsAny(p, "\x00\r\n")
}
func privateDir(p string) error {
	if !cleanAbs(p) {
		return errors.New("invalid local directory")
	}
	st, e := os.Lstat(p)
	if e != nil {
		return e
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.IsDir() || st.Mode().Perm()&0077 != 0 || sys.Uid != uint32(os.Geteuid()) {
		return errors.New("local directory must be private and owned by current user")
	}
	return nil
}
func signer(p string) (ssh.Signer, error) {
	if !cleanAbs(p) {
		return nil, errors.New("invalid key path")
	}
	fd, e := unix.Open(p, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	file := os.NewFile(uintptr(fd), p)
	defer file.Close()
	st, e := file.Stat()
	if e != nil {
		return nil, e
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || sys.Uid != uint32(os.Geteuid()) || st.Size() > 1<<20 {
		return nil, errors.New("key must be a private regular file owned by current user")
	}
	b, e := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if e != nil {
		return nil, e
	}
	defer func() {
		for i := range b {
			b[i] = 0
		}
	}()
	if len(b) > 1<<20 {
		return nil, errors.New("key too large")
	}
	return ssh.ParsePrivateKey(b)
}
func pinned(s string) (ssh.PublicKey, error) {
	if len(s) > 16384 {
		return nil, errors.New("invalid host key")
	}
	k, c, o, r, e := ssh.ParseAuthorizedKey([]byte(s))
	if e != nil || c != "" || len(o) != 0 || len(r) != 0 {
		return nil, errors.New("invalid host key")
	}
	if _, ok := k.(*ssh.Certificate); ok {
		return nil, errors.New("host certificates not supported")
	}
	if strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(k)), "\n") != s {
		return nil, errors.New("noncanonical host key")
	}
	return k, nil
}

// trackedConn records the first close result even when SSH or cancellation
// closes the socket first. Repeated close calls return that same result.
type trackedConn struct {
	net.Conn
	once sync.Once
	err  error
}

func (c *trackedConn) Close() error {
	c.once.Do(func() { c.err = c.Conn.Close() })
	return c.err
}

// Open establishes SSH and a local listener. The sole remote streamlocal
// channel is opened after the first local client connects; channel-open errors
// cancel the forward context and close that connection. Open never invokes a remote command,
// forwards TCP, consults known_hosts, or retries. Cancellation tears down all
// resources, including handshakes in progress. No SSH agent is consulted.
type setupDeps struct {
	dial      func(context.Context, string, string) (net.Conn, error)
	mkdirTemp func(string, string) (string, error)
}

func Open(ctx context.Context, cfg Config) (*Forward, error) {
	return open(ctx, cfg, setupDeps{dial: (&net.Dialer{}).DialContext, mkdirTemp: os.MkdirTemp})
}

func open(ctx context.Context, cfg Config, d setupDeps) (*Forward, error) {
	if ctx == nil || cfg.Timeout <= 0 || cfg.User == "" || !cleanAbs(cfg.RemoteSocket) {
		return nil, errors.New("invalid transport configuration")
	}
	if err := privateDir(cfg.LocalDir); err != nil {
		return nil, err
	}
	key, err := pinned(cfg.HostPublicKey)
	if err != nil {
		return nil, err
	}
	auth, err := signer(cfg.KeyPath)
	if err != nil {
		return nil, err
	}
	setup, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	rawConn, err := d.dial(setup, "tcp", cfg.Address)
	if err != nil {
		if setup.Err() != nil {
			return nil, setup.Err()
		}
		return nil, err
	}
	conn := &trackedConn{Conn: rawConn}
	stop := context.AfterFunc(setup, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(cfg.Timeout))
	cc, chans, reqs, err := ssh.NewClientConn(conn, cfg.Address, &ssh.ClientConfig{User: cfg.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(auth)}, HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
		if !bytes.Equal(k.Marshal(), key.Marshal()) {
			return errors.New("pinned host key mismatch")
		}
		return nil
	}})
	if err != nil {
		closeErr := conn.Close()
		if setup.Err() != nil {
			return nil, errors.Join(setup.Err(), cleanupError(closeErr))
		}
		return nil, errors.Join(fmt.Errorf("SSH handshake: %w", err), cleanupError(closeErr))
	}
	client := ssh.NewClient(cc, chans, reqs)
	if setup.Err() != nil {
		closeErr := client.Close()
		return nil, errors.Join(setup.Err(), cleanupError(errors.Join(closeErr, conn.Close())))
	}
	_ = conn.SetDeadline(time.Time{})
	dir, err := d.mkdirTemp(cfg.LocalDir, "agent-forward-")
	if err != nil {
		closeErr := client.Close()
		return nil, errors.Join(err, cleanupError(errors.Join(closeErr, conn.Close())))
	}
	fctx, fcancel := context.WithCancel(ctx)
	f := &Forward{ctx: fctx, cancel: fcancel, client: client, conn: conn, dir: dir}
	addr := &net.UnixAddr{Name: f.Socket(), Net: "unix"}
	f.listener, err = net.ListenUnix("unix", addr)
	if err != nil {
		return nil, errors.Join(err, cleanupError(f.Close()))
	}
	// Remove explicitly during Close so filesystem cleanup failures are visible.
	f.listener.SetUnlinkOnClose(false)
	if err = os.Chmod(f.Socket(), 0600); err != nil {
		return nil, errors.Join(err, cleanupError(f.Close()))
	}
	// Stop the setup cancellation before publishing; if it already fired, fail.
	if !stop() || setup.Err() != nil {
		err := setup.Err()
		if err == nil {
			err = context.Canceled
		}
		return nil, errors.Join(err, cleanupError(f.Close()))
	}
	go func() { <-f.ctx.Done(); f.Close() }()
	go func() { _ = client.Wait(); f.Close() }()
	go f.serve(cfg.RemoteSocket, cfg.Timeout)
	return f, nil
}

// timedWriter closes the entire forward if any write blocks for the configured
// timeout. SSH channels do not support per-write deadlines.
type timedWriter struct {
	w       io.Writer
	timeout time.Duration
	close   func()
}

func (w timedWriter) Write(p []byte) (int, error) {
	timer := time.AfterFunc(w.timeout, w.close)
	n, err := w.w.Write(p)
	timer.Stop()
	return n, err
}

func (f *Forward) serve(socket string, timeout time.Duration) {
	c, err := f.listener.AcceptUnix()
	if err != nil {
		f.Close()
		return
	}
	f.mu.Lock()
	if f.ctx.Err() != nil {
		f.mu.Unlock()
		_ = c.Close()
		return
	}
	f.local = c
	_ = f.listener.Close()
	f.mu.Unlock()
	// A blocked channel open is bounded by closing the SSH connection. There is
	// no remote channel at all before the single local connection is accepted.
	timer := time.AfterFunc(timeout, func() { f.Close() })
	remote, err := f.client.Dial("unix", socket)
	timer.Stop()
	if err != nil {
		f.Close()
		return
	}
	f.mu.Lock()
	if f.ctx.Err() != nil {
		f.mu.Unlock()
		_ = remote.Close()
		return
	}
	f.remote = remote
	f.mu.Unlock()
	done := make(chan struct{}, 2)
	abort := func() { f.Close() }
	go func() { _, _ = io.Copy(timedWriter{remote, timeout, abort}, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(timedWriter{c, timeout, abort}, remote); done <- struct{}{} }()
	<-done
	f.Close()
	<-done
}
