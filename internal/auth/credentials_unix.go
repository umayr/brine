//go:build linux || darwin

package auth

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var errCredentialState = errors.New("unsafe or unavailable credential state")

// Linux and macOS support these descriptor-relative primitives. Do not substitute
// path-based chmod/read/write or EvalSymlinks: those reopen a link-following race.
// Like secretbindings.Store, we reject writable ancestors (even sticky /tmp) and
// symlink aliases (including macOS /var). Provision a canonical private state path.
// A directory flock serializes cooperating Brine processes, including first use
// and missing-secret fill. This is not containment of hostile same-UID writers.
type credentialState struct {
	dir    *os.File
	loaded *unix.Stat_t
}

func credentialDirSafe(fd int, private bool) bool {
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return false
	}
	if st.Uid != 0 && st.Uid != uint32(os.Geteuid()) {
		return false
	}
	return st.Mode&0022 == 0 && (!private || (st.Uid == uint32(os.Geteuid()) && st.Mode&07777 == 0700)) && credentialACLSafe(fd)
}

func openCredentialState(path string) (*credentialState, error) {
	abs, err := filepath.Abs(path)
	if err != nil || abs == "/" {
		return nil, errCredentialState
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errCredentialState
	}
	if !credentialDirSafe(fd, false) {
		unix.Close(fd)
		return nil, errCredentialState
	}
	parts := strings.Split(strings.TrimPrefix(abs, "/"), "/")
	for i, part := range parts {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e == unix.ENOENT {
			if e = unix.Mkdirat(fd, part, 0700); e == nil || e == unix.EEXIST {
				next, e = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		unix.Close(fd)
		if e != nil {
			return nil, errCredentialState
		}
		fd = next
		if !credentialDirSafe(fd, i == len(parts)-1) {
			unix.Close(fd)
			return nil, errCredentialState
		}
	}
	// Lock the already validated descriptor, not a path or attacker-planted lock
	// file. Closing it releases the lock, including all failure paths.
	if unix.Flock(fd, unix.LOCK_EX) != nil || !credentialDirSafe(fd, true) {
		unix.Close(fd)
		return nil, errCredentialState
	}
	return &credentialState{dir: os.NewFile(uintptr(fd), "credential state")}, nil
}

func (s *credentialState) close() { s.dir.Close() }

func credentialFileSafe(st *unix.Stat_t) bool {
	return st.Mode&unix.S_IFMT == unix.S_IFREG && st.Mode&07777 == 0600 && st.Uid == uint32(os.Geteuid()) && st.Nlink == 1
}

func (s *credentialState) read() ([]byte, bool, error) {
	if !credentialDirSafe(int(s.dir.Fd()), true) {
		return nil, false, errCredentialState
	}
	fd, err := unix.Openat(int(s.dir.Fd()), "credentials.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, errCredentialState
	}
	f := os.NewFile(uintptr(fd), "credentials")
	defer f.Close()
	var before, after unix.Stat_t
	if unix.Fstat(fd, &before) != nil || !credentialFileSafe(&before) || !credentialACLSafe(fd) {
		return nil, false, errCredentialState
	}
	data, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err != nil || len(data) > 64*1024 || unix.Fstat(fd, &after) != nil || !credentialFileSafe(&after) || before.Size != after.Size || before.Mtim != after.Mtim || !credentialACLSafe(fd) {
		return nil, false, errCredentialState
	}
	s.loaded = &after
	if !s.unchanged() {
		return nil, false, errCredentialState
	}
	return data, true, nil
}

func (s *credentialState) unchanged() bool {
	var st unix.Stat_t
	err := unix.Fstatat(int(s.dir.Fd()), "credentials.json", &st, unix.AT_SYMLINK_NOFOLLOW)
	if s.loaded == nil {
		return err == unix.ENOENT
	}
	if err != nil || !credentialFileSafe(&st) || st.Dev != s.loaded.Dev || st.Ino != s.loaded.Ino || st.Size != s.loaded.Size || st.Mtim != s.loaded.Mtim {
		return false
	}
	fd, err := unix.Openat(int(s.dir.Fd()), "credentials.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	var opened unix.Stat_t
	return unix.Fstat(fd, &opened) == nil && credentialFileSafe(&opened) && opened.Dev == st.Dev && opened.Ino == st.Ino && credentialACLSafe(fd)
}

func (s *credentialState) write(data []byte) error {
	if !credentialDirSafe(int(s.dir.Fd()), true) {
		return errCredentialState
	}
	n, err := randomSecret(18)
	if err != nil {
		return errCredentialState
	}
	name := ".credentials-" + n
	dir := int(s.dir.Fd())
	fd, err := unix.Openat(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return errCredentialState
	}
	f := os.NewFile(uintptr(fd), "credential temporary file")
	defer f.Close()
	defer unix.Unlinkat(dir, name, 0)
	if writeCredentialTemporary(f, data) != nil || !s.unchanged() {
		return errCredentialState
	}
	if s.loaded == nil {
		// linkat is a portable atomic no-clobber publish; a competing entry is
		// never overwritten. Remove the temporary second link before returning.
		if unix.Linkat(dir, name, dir, "credentials.json", 0) != nil || unix.Unlinkat(dir, name, 0) != nil {
			return errCredentialState
		}
	} else if unix.Renameat(dir, name, dir, "credentials.json") != nil {
		return errCredentialState
	}
	if unix.Fsync(dir) != nil {
		return errCredentialState
	}
	return nil
}

// Called only for an exclusively-created private temporary descriptor. Check
// inherited ACLs BEFORE any sensitive bytes, not only after publish/chmod. We
// never strip existing ACLs or repair a preexisting/external credential target.
func writeCredentialTemporary(f *os.File, data []byte) error {
	fd := int(f.Fd())
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || !credentialACLSafe(fd) {
		return errCredentialState
	}
	// Only this new descriptor may be chmodded to accommodate a restrictive
	// umask. Chmod does not remove Darwin/default-inherited ACL grants.
	if unix.Fchmod(fd, 0600) != nil || unix.Fstat(fd, &st) != nil || !credentialFileSafe(&st) || !credentialACLSafe(fd) {
		return errCredentialState
	}
	if _, err := f.Write(data); err != nil || f.Sync() != nil {
		return errCredentialState
	}
	return nil
}
