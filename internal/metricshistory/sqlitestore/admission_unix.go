//go:build linux || darwin

package sqlitestore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func trustedDirectory(mode, uid, euid uint32, private bool) bool {
	return mode&unix.S_IFMT == unix.S_IFDIR && (uid == 0 || uid == euid) && mode&0022 == 0 && (!private || (uid == euid && mode&07777 == 0700))
}

// openHistoryDir is an unmounted admission candidate, not a Store open. The
// caller owns the returned descriptor. Path and ancestors must remain stable;
// same-UID/admin actors, ACLs and filesystem semantics remain trusted.
func openHistoryDir(path string) (*os.File, error) {
	unsafe := errors.New("unsafe history directory")
	if !filepath.IsAbs(path) || path == "/" || filepath.Clean(path) != path {
		return nil, unsafe
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, unsafe
	}
	valid := func(fd int, private bool) bool {
		var st unix.Stat_t
		return unix.Fstat(fd, &st) == nil && trustedDirectory(uint32(st.Mode), st.Uid, uint32(os.Geteuid()), private)
	}
	if !valid(fd, false) {
		unix.Close(fd)
		return nil, unsafe
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return nil, unsafe
		}
		fd = next
		if !valid(fd, i == len(parts)-1) {
			unix.Close(fd)
			return nil, unsafe
		}
	}
	return os.NewFile(uintptr(fd), path), nil
}
