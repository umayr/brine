package targetregistry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

const initializedMarker = "brine-target-registry-v2\n"
const emptyRegistry = "{\n  \"version\": 2,\n  \"targets\": []\n}\n"

// ErrInitUncertain means initialization or its rollback encountered an I/O
// error. Stop and quarantine all consumers; automatic cross-process quarantine
// cannot be guaranteed after sync, rollback or final-lock-close failures.
var ErrInitUncertain = errors.New("registry initialization outcome uncertain; quarantine all consumers")

type initHooks struct { // test-only fault injection and synchronization
	syncFile     func(string, *os.File) error
	syncDir      func(string, *os.File) error
	closeFile    func(string, *os.File) error
	removeMarker func(string) error
}

// Init explicitly creates a registry in an existing empty private directory.
// It never repairs partial state. All ancestors and the directory itself must
// be controlled by the operator and stable. On an uncertain error quarantine
// all consumers and reconcile out of band before access or retry.
func Init(dir string) error { return initRegistry(dir, initHooks{}) }

func initRegistry(dir string, hooks initHooks) (result error) {
	if err := checkPrivateDir(dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("registry directory must be empty")
	}
	syncFile := hooks.syncFile
	if syncFile == nil {
		syncFile = func(_ string, f *os.File) error { return f.Sync() }
	}
	syncDir := hooks.syncDir
	if syncDir == nil {
		syncDir = func(_ string, f *os.File) error { return f.Sync() }
	}
	closeFile := hooks.closeFile
	if closeFile == nil {
		closeFile = func(_ string, f *os.File) error { return f.Close() }
	}
	removeMarker := hooks.removeMarker
	if removeMarker == nil {
		removeMarker = os.Remove
	}
	openExclusive := func(name string) (*os.File, error) {
		fd, err := unix.Open(filepath.Join(dir, name), unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), name), nil
	}
	syncDirectory := func(stage string) error {
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		return errors.Join(syncDir(stage, d), closeFile("directory", d))
	}
	lock, err := openExclusive(".lock")
	if err != nil {
		return fmt.Errorf("initialize registry lock: %w", err)
	}
	markerPublished := false
	defer func() {
		if result != nil && markerPublished {
			// Best effort while the lock is still held. If either removal or sync
			// fails, only operator quarantine can prevent another process opening.
			removeErr := removeMarker(filepath.Join(dir, ".initialized"))
			result = errors.Join(result, removeErr, syncDirectory("rollback"))
		}
		if err := closeFile("lock", lock); err != nil {
			result = errors.Join(result, err)
		}
		if result != nil {
			result = errors.Join(ErrInitUncertain, result)
		}
	}()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	if err := syncFile("lock", lock); err != nil {
		return err
	}
	if err := syncDirectory("lock"); err != nil {
		return err
	}
	writeNew := func(name, content string) error {
		f, err := openExclusive(name)
		if err != nil {
			return err
		}
		_, writeErr := io.WriteString(f, content)
		if writeErr == nil {
			writeErr = syncFile(name, f)
		}
		return errors.Join(writeErr, closeFile(name, f))
	}
	if err := writeNew("targets.json", emptyRegistry); err != nil {
		return err
	}
	if err := syncDirectory("data"); err != nil {
		return err
	}
	// The marker bytes and data are durable before the completion name is
	// published. A leftover pending file makes a crash/partial init fail closed.
	if err := writeNew(".initialized-pending", initializedMarker); err != nil {
		return err
	}
	if err := syncDirectory("pending"); err != nil {
		return err
	}
	if err := os.Link(filepath.Join(dir, ".initialized-pending"), filepath.Join(dir, ".initialized")); err != nil {
		return err
	}
	markerPublished = true
	if err := syncDirectory("marker"); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, ".initialized-pending")); err != nil {
		return err
	}
	if err := syncDirectory("complete"); err != nil {
		return err
	}
	return nil
}

func checkPrivateDir(dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || dir == "/" {
		return errors.New("registry directory must be absolute and clean")
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.IsDir() || st.Mode().Perm()&0077 != 0 || sys.Uid != uint32(os.Geteuid()) {
		return errors.New("registry directory must be existing, private and owned by the current user")
	}
	return nil
}

// OpenExisting opens only an explicitly initialized registry. It never creates
// state, and checks the marker, lock and registry data on every operation.
func OpenExisting(dir string) (*Registry, error) { return openExisting(dir, nil) }

func openExisting(dir string, onLockWait func()) (*Registry, error) {
	if err := checkPrivateDir(dir); err != nil {
		return nil, err
	}
	r := &Registry{dir: dir, strict: true, onLockWait: onLockWait}
	f, err := r.lock(context.Background(), true)
	if err != nil {
		return nil, err
	}
	r.lockIdentity, err = f.Stat()
	if err == nil {
		_, err = r.load()
	}
	err = errors.Join(err, r.closeLock(f))
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) checkMarker() error {
	if _, err := os.Lstat(filepath.Join(r.dir, ".initialized-pending")); err == nil {
		return errors.New("incomplete registry initialization")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fd, err := unix.Open(filepath.Join(r.dir, ".initialized"), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "marker")
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() != int64(len(initializedMarker)) {
		return errors.New("invalid registry initialization marker")
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(len(initializedMarker)+1)))
	if err != nil || string(b) != initializedMarker {
		return errors.New("invalid registry initialization marker")
	}
	return nil
}
