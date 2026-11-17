// Package targetregistry stores operator-confirmed target pins. It does not connect
// to hosts or establish that observations supplied by a caller are authentic.
package targetregistry

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"path"
	"sync"
	"time"
	"unicode/utf8"

	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/umayr/brine/internal/agentclient"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

var (
	ErrNotFound    = errors.New("target not found")
	ErrConflict    = errors.New("target already exists or changed")
	ErrUnconfirmed = errors.New("operator confirmation required")
	ErrMismatch    = errors.New("target identity unavailable or mismatched")
	token          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	hostLabel      = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
)

// Record contains public metadata and a private-key path, never private key bytes.
// HostPublicKey is a canonical OpenSSH authorized-key entry without a comment.
type Record struct {
	ID                   string `json:"id"`
	Revision             uint64 `json:"revision"`
	Host                 string `json:"host"`
	Port                 uint16 `json:"port"`
	User                 string `json:"user"`
	KeyPath              string `json:"key_path"`
	HostPublicKey        string `json:"host_public_key"`
	HostKeySHA256        string `json:"host_key_sha256"`
	AgentInstallationID  string `json:"agent_installation_id"`
	EngineID             string `json:"engine_id"`
	RemoteSocket         string `json:"remote_socket"`
	AgentPublicKey       string `json:"agent_public_key"`
	AgentFingerprint     string `json:"agent_fingerprint"`
	EngineKind           string `json:"engine_kind"`
	EngineSocket         string `json:"engine_socket"`
	DaemonID             string `json:"daemon_id"`
	Generation           uint64 `json:"generation"`
	RevealKeyFingerprint string `json:"reveal_key_fingerprint"`
}

// Identity must be obtained from the connected endpoint by a future authenticated
// transport; missing fields are failures, never a request to trust on first use.
type Identity = Record

// Binding returns a fresh agentclient binding; its public-key slice does not
// share storage with the record or other calls.
func (r Record) Binding() (agentclient.Binding, error) {
	if err := r.Validate(); err != nil {
		return agentclient.Binding{}, err
	}
	key, _ := base64.RawURLEncoding.DecodeString(r.AgentPublicKey)
	return agentclient.Binding{AgentPublicKey: ed25519.PublicKey(key), EngineID: r.EngineID, Generation: r.Generation, EngineKind: r.EngineKind, EngineSocket: r.EngineSocket, DaemonID: r.DaemonID}, nil
}

// VerifiedRevealKey returns a private copy of the supplied public key only if
// its raw Ed25519 SHA-256 fingerprint matches this complete validated record.
// Pass the returned copy, not the input, to agentclient.NewWithRevealKey.
func (r Record) VerifiedRevealKey(key ed25519.PublicKey) (ed25519.PublicKey, error) {
	if r.Validate() != nil || len(key) != ed25519.PublicKeySize {
		return nil, ErrMismatch
	}
	copyKey := append(ed25519.PublicKey(nil), key...)
	h := sha256.Sum256(copyKey)
	fingerprint := "SHA256:" + base64.RawStdEncoding.EncodeToString(h[:])
	if subtle.ConstantTimeCompare([]byte(fingerprint), []byte(r.RevealKeyFingerprint)) != 1 {
		return nil, ErrMismatch
	}
	return copyKey, nil
}

func safeText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, c := range s {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

func cleanPath(s string) bool {
	return len(s) <= 4096 && utf8.ValidString(s) && path.IsAbs(s) && path.Clean(s) == s && s != "/" && !strings.ContainsAny(s, "\x00\r\n?#")
}
func validAgent(key, id, fingerprint string) bool {
	b, err := base64.RawURLEncoding.Strict().DecodeString(key)
	if err != nil || len(b) != ed25519.PublicKeySize || base64.RawURLEncoding.EncodeToString(b) != key {
		return false
	}
	h := sha256.Sum256(b)
	return id == hex.EncodeToString(h[:]) && fingerprint == "SHA256:"+base64.RawStdEncoding.EncodeToString(h[:])
}
func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validFingerprint(s string) bool {
	if !strings.HasPrefix(s, "SHA256:") {
		return false
	}
	v := strings.TrimPrefix(s, "SHA256:")
	b, err := base64.RawStdEncoding.Strict().DecodeString(v)
	return err == nil && len(b) == 32 && base64.RawStdEncoding.EncodeToString(b) == v
}
func validHostKey(key, fingerprint string) bool {
	if len(key) > 16384 || !validFingerprint(fingerprint) {
		return false
	}
	pub, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil || comment != "" || len(options) != 0 || len(rest) != 0 {
		return false
	}
	// Reject certificates and noncanonical encodings, options, comments and newlines.
	if _, ok := pub.(*ssh.Certificate); ok {
		return false
	}
	return strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(pub)), "\n") == key && ssh.FingerprintSHA256(pub) == fingerprint
}
func validHost(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	if net.ParseIP(s) != nil {
		return true
	}
	for _, l := range strings.Split(s, ".") {
		if !hostLabel.MatchString(l) {
			return false
		}
	}
	return true
}
func (r Record) Validate() error {
	for _, s := range []string{r.ID, r.Host, r.User, r.KeyPath, r.HostPublicKey, r.HostKeySHA256, r.AgentInstallationID, r.EngineID, r.RemoteSocket, r.AgentPublicKey, r.AgentFingerprint, r.EngineKind, r.EngineSocket, r.DaemonID, r.RevealKeyFingerprint} {
		if !safeText(s) {
			return errors.New("invalid target record text")
		}
	}
	if r.Revision == 0 || !token.MatchString(r.ID) || !validHost(r.Host) || r.Port == 0 || !token.MatchString(r.User) ||
		!filepath.IsAbs(r.KeyPath) || filepath.Clean(r.KeyPath) != r.KeyPath || len(r.KeyPath) > 4096 || strings.ContainsAny(r.KeyPath, "\x00\r\n") ||
		!validHostKey(r.HostPublicKey, r.HostKeySHA256) || !validAgent(r.AgentPublicKey, r.AgentInstallationID, r.AgentFingerprint) || !validUUID(r.EngineID) ||
		!cleanPath(r.RemoteSocket) || !strings.HasPrefix(r.EngineSocket, "unix://") || !cleanPath(strings.TrimPrefix(r.EngineSocket, "unix://")) ||
		(r.EngineKind != "docker" && r.EngineKind != "podman") || r.Generation == 0 || r.DaemonID == "" || len(r.DaemonID) > 4096 || !utf8.ValidString(r.DaemonID) || strings.ContainsAny(r.DaemonID, "\x00\r\n") || !validFingerprint(r.RevealKeyFingerprint) {
		return errors.New("invalid target record")
	}
	return nil
}
func (r Record) Verify(i Identity) error {
	if r.Validate() != nil || i.Validate() != nil || r != i {
		return ErrMismatch
	}
	return nil
}

type disk struct {
	Version int      `json:"version"`
	Targets []Record `json:"targets"`
}

// Registry uses an operator-chosen private directory. All cooperating processes
// must use this API and the same canonical directory.
type Registry struct{ dir string }

func Open(dir string) (*Registry, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, errors.New("registry directory must be absolute and clean")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("registry directory must be private")
	}
	return &Registry{dir: dir}, nil
}

// ReadLease holds the shared kernel lock until Close. It must not be copied.
// Keep it open until all work using the returned record, including streams, ends.
type ReadLease struct {
	once sync.Once
	file *os.File
	err  error
}

// Close releases the lease. Concurrent and repeated calls are safe.
func (l *ReadLease) Close() error {
	l.once.Do(func() { l.err = l.file.Close() })
	return l.err
}

func (r *Registry) lock(ctx context.Context, shared bool) (*os.File, error) {
	return r.lockFile(ctx, shared, !shared)
}

func (r *Registry) lockFile(ctx context.Context, shared, create bool) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if create {
		flags |= unix.O_CREAT
	}
	fd, err := unix.Open(filepath.Join(r.dir, ".lock"), flags, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "lock")
	fail := func(err error) (*os.File, error) { f.Close(); return nil, err }
	st, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return fail(errors.New("invalid registry lock file"))
	}
	mode := unix.LOCK_EX
	if shared {
		mode = unix.LOCK_SH
	}
	for {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		err = unix.Flock(fd, mode|unix.LOCK_NB)
		if err == nil {
			if err := ctx.Err(); err != nil {
				return fail(err)
			}
			return f, nil
		}
		if err == unix.EINTR {
			continue
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			return fail(err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return fail(ctx.Err())
		case <-timer.C:
		}
	}
}
func (r *Registry) lockedContext(ctx context.Context, fn func() error) error {
	f, err := r.lock(ctx, false)
	if err != nil {
		return err
	}
	defer f.Close()
	return fn()
}
func (r *Registry) locked(fn func() error) error { return r.lockedContext(context.Background(), fn) }

// AcquireRead returns an exact validated value and a shared-lock lease. The
// caller must close the lease only after the entire operation/stream completes.
// The lease protects against cooperating writers, not direct filesystem edits.
func (r *Registry) AcquireRead(ctx context.Context, id string) (Record, *ReadLease, error) {
	f, err := r.lock(ctx, true)
	if err != nil {
		return Record{}, nil, err
	}
	lease := &ReadLease{file: f}
	d, err := r.load()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		lease.Close()
		return Record{}, nil, err
	}
	for _, t := range d.Targets {
		if t.ID == id {
			return t, lease, nil
		}
	}
	lease.Close()
	return Record{}, nil, ErrNotFound
}
func (r *Registry) load() (disk, error) {
	d := disk{Version: 2, Targets: []Record{}}
	fd, err := unix.Open(filepath.Join(r.dir, "targets.json"), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	f := os.NewFile(uintptr(fd), "targets")
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return d, err
	}
	if !st.Mode().IsRegular() || st.Size() > 4<<20 || st.Mode().Perm()&0077 != 0 {
		return d, errors.New("invalid registry file")
	}
	b, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if err != nil {
		return d, err
	}
	if len(b) > 4<<20 {
		return d, errors.New("registry too large")
	}
	// Reject duplicate JSON object keys as well as unknown fields and trailing values.
	if err = rejectDuplicateKeys(b); err != nil {
		return d, err
	}
	d = disk{}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&d); err != nil {
		return d, err
	}
	if dec.Decode(new(any)) != io.EOF {
		return d, errors.New("trailing registry data")
	}
	if d.Version != 2 || d.Targets == nil {
		return d, errors.New("invalid registry version or targets")
	}
	canonical, err := json.MarshalIndent(d, "", "  ")
	if err != nil || !bytes.Equal(b, append(canonical, '\n')) {
		return d, errors.New("noncanonical registry JSON")
	}
	seen := map[string]bool{}
	for _, t := range d.Targets {
		if t.Validate() != nil || seen[t.ID] {
			return d, errors.New("invalid or duplicate target")
		}
		seen[t.ID] = true
	}
	return d, nil
}
func rejectDuplicateKeys(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	var walk func(string) error
	walk = func(kind string) error {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			if kind != "envelope" && kind != "record" {
				return errors.New("unexpected object")
			}
			seen := map[string]bool{}
			for dec.More() {
				k, err := dec.Token()
				if err != nil {
					return err
				}
				s, ok := k.(string)
				allowed := kind == "envelope" && (s == "version" || s == "targets") || kind == "record" && (s == "id" || s == "revision" || s == "host" || s == "port" || s == "user" || s == "key_path" || s == "host_public_key" || s == "host_key_sha256" || s == "agent_installation_id" || s == "engine_id" || s == "remote_socket" || s == "agent_public_key" || s == "agent_fingerprint" || s == "engine_kind" || s == "engine_socket" || s == "daemon_id" || s == "generation" || s == "reveal_key_fingerprint")
				if !ok || !allowed || seen[s] {
					return errors.New("noncanonical, unknown or duplicate JSON key")
				}
				seen[s] = true
				child := "scalar"
				if kind == "envelope" && s == "targets" {
					child = "targets"
				}
				if err = walk(child); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case '[':
			if kind != "targets" {
				return errors.New("unexpected array")
			}
			for dec.More() {
				if err := walk("record"); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
		return errors.New("invalid JSON")
	}
	return walk("envelope")
}
func (r *Registry) save(d disk) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if len(b) > 4<<20 {
		return errors.New("registry too large")
	}
	f, err := os.CreateTemp(r.dir, ".targets-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, filepath.Join(r.dir, "targets.json")); err != nil {
		return err
	}
	dir, err := os.Open(r.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// List fails closed if any record or the storage envelope is malformed.
func (r *Registry) List() ([]Record, error) { return r.ListContext(context.Background()) }

// ListContext loads a validated snapshot under a shared lock. Its context
// bounds lock acquisition, not filesystem I/O after acquisition.
func (r *Registry) ListContext(ctx context.Context) ([]Record, error) {
	f, err := r.lockFile(ctx, true, true)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	d, err := r.load()
	if err != nil {
		return nil, err
	}
	return d.Targets, nil
}
func (r *Registry) Get(id string) (Record, error) { return r.GetContext(context.Background(), id) }

// GetContext returns a validated snapshot, but does not pin it after return.
func (r *Registry) GetContext(ctx context.Context, id string) (Record, error) {
	ts, err := r.ListContext(ctx)
	if err != nil {
		return Record{}, err
	}
	for _, t := range ts {
		if t.ID == id {
			return t, nil
		}
	}
	return Record{}, ErrNotFound
}

// Add requires the exact reviewed record; duplicate IDs never overwrite.
func (r *Registry) Add(t Record, reviewed Record) error {
	return r.AddContext(context.Background(), t, reviewed)
}

// AddContext is Add with a cancellable lock wait.
func (r *Registry) AddContext(ctx context.Context, t Record, reviewed Record) error {
	if t != reviewed {
		return ErrUnconfirmed
	}
	if err := t.Validate(); err != nil {
		return err
	}
	if t.Revision != 1 {
		return ErrConflict
	}
	return r.lockedContext(ctx, func() error {
		d, e := r.load()
		if e != nil {
			return e
		}
		for _, v := range d.Targets {
			if v.ID == t.ID {
				return ErrConflict
			}
		}
		d.Targets = append(d.Targets, t)
		return r.save(d)
	})
}

// Update uses the complete previously reviewed record as a compare-and-swap;
// changing an ID is not an update. The next revision must increment by one;
// reviewed must be an exact copy of the record the operator approved.
// ReviewedUpdate is a value snapshot of both sides presented for approval.
// It is not an authentication token; callers must protect the review flow.
type ReviewedUpdate struct{ Previous, Next Record }

func (r *Registry) Update(old, next Record, reviewed ReviewedUpdate) error {
	return r.UpdateContext(context.Background(), old, next, reviewed)
}

// UpdateContext is Update with a cancellable lock wait.
func (r *Registry) UpdateContext(ctx context.Context, old, next Record, reviewed ReviewedUpdate) error {
	if old != reviewed.Previous || next != reviewed.Next {
		return ErrUnconfirmed
	}
	if err := old.Validate(); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if old.Revision == ^uint64(0) || next.Revision != old.Revision+1 {
		return ErrConflict
	}
	if old.ID != next.ID {
		return fmt.Errorf("%w: cannot change ID", ErrConflict)
	}
	return r.lockedContext(ctx, func() error {
		d, e := r.load()
		if e != nil {
			return e
		}
		for i, v := range d.Targets {
			if v.ID == old.ID {
				if v != old {
					return ErrConflict
				}
				d.Targets[i] = next
				return r.save(d)
			}
		}
		return ErrNotFound
	})
}
