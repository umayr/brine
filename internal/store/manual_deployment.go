package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// ManualDeploymentIntent contains only immutable recovery bindings, never
// credentials, snapshots or engine output. Unknown requests remain unresolved.
type ManualDeploymentIntent struct {
	Host, Target, Stack, Mode, Request, PriorRequest, Journal, Selection, Stage, Manifest string
	Resolved                                                                              bool
}
type ManualDeploymentStore interface {
	SaveManualIntent(ManualDeploymentIntent) error
	GetManualIntent(string) (ManualDeploymentIntent, error)
	ListManualIntents() ([]ManualDeploymentIntent, error)
	ResolveManualIntent(ManualDeploymentIntent) error
}

var ErrManualIntent = errors.New("manual deployment recovery unavailable or conflicting")

const manualIntentFile = "manual-deployments.json"
const maxManualIntents = 4096
const maxManualIntentBytes = 4 << 20

func (i ManualDeploymentIntent) valid() bool {
	safe := func(s string) bool {
		return s != "" && len(s) <= 128 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "/\\\x00\r\n")
	}
	if !safe(i.Host) || !safe(i.Stack) || !remoteGitHex(i.Target, 32) || !remoteGitHex(i.Request, 16) || !remoteGitHex(i.Journal, 16) || !strings.HasPrefix(i.Selection, "sha256:") || !remoteGitHex(strings.TrimPrefix(i.Selection, "sha256:"), 32) {
		return false
	}
	switch i.Mode {
	case "create", "update":
		return i.PriorRequest == "" && remoteGitHex(i.Stage, 16) && remoteGitHex(i.Manifest, 32)
	case "remove":
		return remoteGitHex(i.PriorRequest, 16) && i.PriorRequest != i.Request && i.Stage == "" && i.Manifest == ""
	}
	return false
}

// All access is rooted at an opened non-symlink directory and serialized across
// FSStore instances/processes. A missing ledger is only an initial empty store;
// corrupt/unreadable state never authorizes a mutation. The independent
// initialization anchor detects loss of both ledger and sidecar. The trusted
// data root (including its initialization anchor) must not be deleted/replaced;
// destruction of all trusted state is not distinguishable from first provision.
func (s *FSStore) manualTransaction(change func(*[]ManualDeploymentIntent) error, write bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := unix.Open(s.dataDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrManualIntent
	}
	defer unix.Close(dir)
	// Lock the trusted root inode, not a replaceable sidecar inode.
	if unix.Flock(dir, unix.LOCK_EX|unix.LOCK_NB) != nil {
		return ErrManualIntent
	}
	defer unix.Flock(dir, unix.LOCK_UN)
	anchorErr := unix.Mkdirat(dir, "manual-deployments.initialized", 0700)
	initialized := errors.Is(anchorErr, unix.EEXIST)
	if anchorErr != nil && !initialized {
		return ErrManualIntent
	}
	anchor, anchorErr := unix.Openat(dir, "manual-deployments.initialized", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if anchorErr != nil {
		return ErrManualIntent
	}
	defer unix.Close(anchor)
	if unix.Fsync(dir) != nil {
		return ErrManualIntent
	}
	lock, err := unix.Openat(dir, "manual-deployments.lock", unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	fresh := err == nil
	if fresh && initialized {
		unix.Close(lock)
		return ErrManualIntent
	}
	if errors.Is(err, unix.EEXIST) {
		lock, err = unix.Openat(dir, "manual-deployments.lock", unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return ErrManualIntent
	}
	defer unix.Close(lock)
	var ls unix.Stat_t
	if unix.Fstat(lock, &ls) != nil || ls.Mode&unix.S_IFMT != unix.S_IFREG || ls.Mode&0077 != 0 {
		return ErrManualIntent
	}
	// The sidecar is now only a provision marker; replacing it cannot split
	// serialization because every transaction holds the root directory lock.
	rows := []ManualDeploymentIntent{}
	fd, err := unix.Openat(dir, manualIntentFile, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == nil {
		f := os.NewFile(uintptr(fd), manualIntentFile)
		defer f.Close()
		info, e := f.Stat()
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxManualIntentBytes {
			return ErrManualIntent
		}
		raw, e := io.ReadAll(io.LimitReader(f, maxManualIntentBytes+1))
		if e != nil || len(raw) > maxManualIntentBytes {
			return ErrManualIntent
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if dec.Decode(&rows) != nil || rows == nil {
			return ErrManualIntent
		}
		// This private ledger is written only by this serializer. Require its
		// canonical shape, rejecting duplicate/aliased/omitted fields too.
		canonical, marshalErr := json.Marshal(rows)
		var compact bytes.Buffer
		if marshalErr != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(canonical, compact.Bytes()) {
			return ErrManualIntent
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			return ErrManualIntent
		}
	} else if !errors.Is(err, unix.ENOENT) || !fresh {
		return ErrManualIntent
	} else {
		write = true
	}
	if len(rows) > maxManualIntents {
		return ErrManualIntent
	}
	ids := map[string]bool{}
	active := map[string]bool{}
	for _, r := range rows {
		if !r.valid() || ids[r.Request] || (!r.Resolved && active[r.Stack]) {
			return ErrManualIntent
		}
		ids[r.Request] = true
		if !r.Resolved {
			active[r.Stack] = true
		}
	}
	if err := change(&rows); err != nil {
		return err
	}
	if !write {
		return nil
	}
	raw, err := json.Marshal(rows)
	if err != nil || len(raw) > maxManualIntentBytes {
		return ErrManualIntent
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return ErrManualIntent
	}
	tmp := ".manual-deployments-" + hex.EncodeToString(nonce[:])
	defer unix.Unlinkat(dir, tmp, 0)
	fd, err = unix.Openat(dir, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return ErrManualIntent
	}
	f := os.NewFile(uintptr(fd), tmp)
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return ErrManualIntent
	}
	if unix.Renameat(dir, tmp, dir, manualIntentFile) != nil || unix.Fsync(dir) != nil {
		return ErrManualIntent
	}
	return nil
}
func (s *FSStore) SaveManualIntent(i ManualDeploymentIntent) error {
	if !i.valid() || i.Resolved {
		return ErrManualIntent
	}
	return s.manualTransaction(func(rows *[]ManualDeploymentIntent) error {
		if len(*rows) >= maxManualIntents {
			return ErrManualIntent
		}
		for _, r := range *rows {
			if r.Request == i.Request || (!r.Resolved && r.Stack == i.Stack) {
				return ErrManualIntent
			}
		}
		*rows = append(*rows, i)
		return nil
	}, true)
}
func (s *FSStore) ListManualIntents() ([]ManualDeploymentIntent, error) {
	var result []ManualDeploymentIntent
	err := s.manualTransaction(func(rows *[]ManualDeploymentIntent) error {
		result = append([]ManualDeploymentIntent(nil), (*rows)...)
		return nil
	}, false)
	return result, err
}
func (s *FSStore) GetManualIntent(id string) (ManualDeploymentIntent, error) {
	rows, err := s.ListManualIntents()
	if err != nil {
		return ManualDeploymentIntent{}, err
	}
	for _, r := range rows {
		if r.Request == id {
			return r, nil
		}
	}
	return ManualDeploymentIntent{}, ErrManualIntent
}
func (s *FSStore) ResolveManualIntent(expected ManualDeploymentIntent) error {
	if !expected.valid() || expected.Resolved {
		return ErrManualIntent
	}
	return s.manualTransaction(func(rows *[]ManualDeploymentIntent) error {
		for n, r := range *rows {
			if r.Request == expected.Request {
				if r != expected {
					return ErrManualIntent
				}
				(*rows)[n].Resolved = true
				return nil
			}
		}
		return ErrManualIntent
	}, true)
}
