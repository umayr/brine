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

const (
	manualIntentFile     = "manual-deployments.json"
	manualIntentSidecar  = "manual-deployments.lock"
	manualIntentAnchor   = "manual-deployments.initialized"
	maxManualIntents     = 4096
	maxManualIntentBytes = 4 << 20
)

func (i ManualDeploymentIntent) valid() bool {
	safe := func(s string) bool {
		return s != "" && len(s) <= 128 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "/\\\x00\r\n")
	}
	if !safe(i.Host) || !safe(i.Stack) || !remoteGitHex(i.Target, 32) || !remoteGitHex(i.Request, 16) || !remoteGitHex(i.Journal, 16) || !strings.HasPrefix(i.Selection, "sha256:") || !remoteGitHex(strings.TrimPrefix(i.Selection, "sha256:"), 32) {
		return false
	}
	switch i.Mode {
	case "create":
		return i.PriorRequest == "" && remoteGitHex(i.Stage, 16) && remoteGitHex(i.Manifest, 32)
	case "update":
		return (i.PriorRequest == "" || (remoteGitHex(i.PriorRequest, 16) && i.PriorRequest != i.Request)) && remoteGitHex(i.Stage, 16) && remoteGitHex(i.Manifest, 32)
	case "remove", "start", "stop", "restart":
		return remoteGitHex(i.PriorRequest, 16) && i.PriorRequest != i.Request && i.Stage == "" && i.Manifest == ""
	}
	return false
}

// Lock the trusted root inode, not a replaceable sidecar inode. Closing the
// returned descriptor releases the flock. This does not acquire s.mu.
func (s *FSStore) lockManualRoot() (int, error) {
	dir, err := unix.Open(s.dataDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, ErrManualIntent
	}
	if unix.Flock(dir, unix.LOCK_EX|unix.LOCK_NB) != nil {
		unix.Close(dir)
		return -1, ErrManualIntent
	}
	return dir, nil
}

func checkManualAnchor(dir int) error {
	fd, err := unix.Openat(dir, manualIntentAnchor, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrManualIntent
	}
	unix.Close(fd)
	return nil
}

// The sidecar is only a provision marker; it is never the lock target.
func checkManualSidecar(dir int, create bool) (fresh bool, err error) {
	const flags = unix.O_RDWR | unix.O_NONBLOCK | unix.O_NOFOLLOW | unix.O_CLOEXEC
	openFlags := flags
	if create {
		openFlags |= unix.O_CREAT | unix.O_EXCL
	}
	fd, err := unix.Openat(dir, manualIntentSidecar, openFlags, 0600)
	fresh = create && err == nil
	if create && errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(dir, manualIntentSidecar, flags, 0)
	}
	if err != nil {
		return false, ErrManualIntent
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0077 != 0 {
		return false, ErrManualIntent
	}
	return fresh, nil
}

// readManualLedger is the bounded decoder for both transaction paths. A nil
// slice with no error means the file is absent, not an initialized empty ledger.
// The caller holds the trusted root flock and decides whether absence is valid.
func readManualLedger(dir int) ([]ManualDeploymentIntent, error) {
	fd, err := unix.Openat(dir, manualIntentFile, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrManualIntent
	}
	f := os.NewFile(uintptr(fd), manualIntentFile)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxManualIntentBytes {
		return nil, ErrManualIntent
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxManualIntentBytes+1))
	if err != nil || len(raw) > maxManualIntentBytes {
		return nil, ErrManualIntent
	}
	var rows []ManualDeploymentIntent
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&rows) != nil || rows == nil || len(rows) > maxManualIntents || dec.Decode(new(any)) != io.EOF {
		return nil, ErrManualIntent
	}
	// This private ledger is written only by this serializer. Require its
	// canonical shape, rejecting duplicate/aliased/omitted fields too.
	canonical, err := json.Marshal(rows)
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(canonical, compact.Bytes()) {
		return nil, ErrManualIntent
	}
	ids := map[string]bool{}
	active := map[string]bool{}
	for _, r := range rows {
		if !r.valid() || ids[r.Request] || (!r.Resolved && active[r.Stack]) {
			return nil, ErrManualIntent
		}
		ids[r.Request] = true
		if !r.Resolved {
			active[r.Stack] = true
		}
	}
	return rows, nil
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
	dir, err := s.lockManualRoot()
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	anchorErr := unix.Mkdirat(dir, manualIntentAnchor, 0700)
	initialized := errors.Is(anchorErr, unix.EEXIST)
	if anchorErr != nil && !initialized {
		return ErrManualIntent
	}
	if checkManualAnchor(dir) != nil || unix.Fsync(dir) != nil {
		return ErrManualIntent
	}
	fresh, err := checkManualSidecar(dir, true)
	if err != nil || (fresh && initialized) {
		return ErrManualIntent
	}
	rows, err := readManualLedger(dir)
	if err != nil {
		return err
	}
	if rows == nil {
		if !fresh {
			return ErrManualIntent
		}
		rows = []ManualDeploymentIntent{}
		write = true
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
	fd, err := unix.Openat(dir, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
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
		for _, r := range *rows {
			if r.Request == i.Request || (!r.Resolved && r.Stack == i.Stack) {
				return ErrManualIntent
			}
		}
		// Lifecycle replay evidence belongs to Dill and the incoming journal.
		// Bound only resolved lifecycle recovery rows; never evict unresolved or
		// existing manual deployment evidence, and check conflicts before pruning.
		retained := make([]ManualDeploymentIntent, 0, len(*rows))
		count := 0
		for j := len(*rows) - 1; j >= 0; j-- {
			r := (*rows)[j]
			life := r.Mode == "start" || r.Mode == "stop" || r.Mode == "restart"
			if r.Resolved && life {
				count++
				if count > 128 {
					continue
				}
			}
			retained = append(retained, r)
		}
		for a, b := 0, len(retained)-1; a < b; a, b = a+1, b-1 {
			retained[a], retained[b] = retained[b], retained[a]
		}
		if len(retained) >= maxManualIntents {
			return ErrManualIntent
		}
		*rows = append(retained, i)
		return nil
	}, true)
}

// existingManualIntentsLocked never provisions or repairs state. The presence
// check and decoding both run under the caller's trusted root flock.
func existingManualIntentsLocked(dir int) ([]ManualDeploymentIntent, error) {
	present := 0
	for _, name := range []string{manualIntentFile, manualIntentSidecar, manualIntentAnchor} {
		var st unix.Stat_t
		err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if err == nil {
			present++
		} else if !errors.Is(err, unix.ENOENT) {
			return nil, ErrManualIntent
		}
	}
	if present == 0 {
		return nil, nil
	}
	if present != 3 || checkManualAnchor(dir) != nil {
		return nil, ErrManualIntent
	}
	if _, err := checkManualSidecar(dir, false); err != nil {
		return nil, err
	}
	rows, err := readManualLedger(dir)
	if err != nil || rows == nil {
		return nil, ErrManualIntent
	}
	return rows, nil
}

// ExistingManualIntents does not create a ledger merely because a History
// tab is opened. Incomplete initialization or loss is not an empty history.
func (s *FSStore) ExistingManualIntents() ([]ManualDeploymentIntent, error) {
	dir, err := s.lockManualRoot()
	if err != nil {
		return nil, err
	}
	defer unix.Close(dir)
	rows, err := existingManualIntentsLocked(dir)
	if len(rows) == 0 {
		return nil, err
	}
	return rows, err
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
