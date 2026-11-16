// Package targetregistry stores operator-confirmed target pins. It does not connect
// to hosts or establish that observations supplied by a caller are authentic.
package targetregistry

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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
	ID                  string `json:"id"`
	Host                string `json:"host"`
	Port                uint16 `json:"port"`
	User                string `json:"user"`
	KeyPath             string `json:"key_path"`
	HostPublicKey       string `json:"host_public_key"`
	HostKeySHA256       string `json:"host_key_sha256"`
	AgentInstallationID string `json:"agent_installation_id"`
	EngineID            string `json:"engine_id"`
}

// Identity must be obtained from the connected endpoint by a future authenticated
// transport; missing fields are failures, never a request to trust on first use.
type Identity struct{ HostKeySHA256, AgentInstallationID, EngineID string }

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
	if !token.MatchString(r.ID) || !validHost(r.Host) || r.Port == 0 || !token.MatchString(r.User) ||
		!filepath.IsAbs(r.KeyPath) || filepath.Clean(r.KeyPath) != r.KeyPath || len(r.KeyPath) > 4096 || strings.ContainsAny(r.KeyPath, "\x00\r\n") ||
		!validHostKey(r.HostPublicKey, r.HostKeySHA256) || !token.MatchString(r.AgentInstallationID) || !token.MatchString(r.EngineID) {
		return errors.New("invalid target record")
	}
	return nil
}
func (r Record) Verify(i Identity) error {
	if r.Validate() != nil || !validFingerprint(i.HostKeySHA256) || !token.MatchString(i.AgentInstallationID) || !token.MatchString(i.EngineID) ||
		r.HostKeySHA256 != i.HostKeySHA256 || r.AgentInstallationID != i.AgentInstallationID || r.EngineID != i.EngineID {
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
func (r *Registry) locked(fn func() error) error {
	// O_NOFOLLOW prevents replacing the coordination file with a symlink.
	fd, err := unix.Open(filepath.Join(r.dir, ".lock"), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "lock")
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return errors.New("invalid registry lock file")
	}
	if err = unix.Flock(fd, unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	return fn()
}
func (r *Registry) load() (disk, error) {
	d := disk{Version: 1, Targets: []Record{}}
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
	if d.Version != 1 || d.Targets == nil {
		return d, errors.New("invalid registry version or targets")
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
				allowed := kind == "envelope" && (s == "version" || s == "targets") || kind == "record" && (s == "id" || s == "host" || s == "port" || s == "user" || s == "key_path" || s == "host_public_key" || s == "host_key_sha256" || s == "agent_installation_id" || s == "engine_id")
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
func (r *Registry) List() (out []Record, err error) {
	err = r.locked(func() error {
		d, e := r.load()
		if e == nil {
			out = d.Targets
		}
		return e
	})
	return
}
func (r *Registry) Get(id string) (Record, error) {
	ts, err := r.List()
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

// Add requires a fresh explicit confirmation; duplicate IDs never overwrite.
func (r *Registry) Add(t Record, confirmed bool) error {
	if !confirmed {
		return ErrUnconfirmed
	}
	if err := t.Validate(); err != nil {
		return err
	}
	return r.locked(func() error {
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
// changing an ID is not an update. Callers must confirm every new value.
func (r *Registry) Update(old, next Record, confirmed bool) error {
	if !confirmed {
		return ErrUnconfirmed
	}
	if err := old.Validate(); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if old.ID != next.ID {
		return fmt.Errorf("%w: cannot change ID", ErrConflict)
	}
	return r.locked(func() error {
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
