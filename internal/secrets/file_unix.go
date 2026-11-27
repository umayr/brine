//go:build linux

package secrets

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Linux O_PATH: inspect inodes without opening a device, FIFO or socket for
// I/O. The procfs fd directory is part of the trusted local OS environment.
const linuxOPath = 0x200000

func init() { readProtectedFile = readLinuxFile }

func readLinuxFile(ctx context.Context, ref FileRef, limit int) ([]byte, error) {
	if !validFileRef(ref) {
		return nil, ErrDenied
	}
	uid := uint32(os.Geteuid())
	fd, err := openFilePath(ctx, ref, uid)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)

	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil || !safeFile(&st, uid) {
		return nil, ErrDenied
	}
	if st.Size > int64(limit) {
		return nil, ErrTooLarge
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	// Reopen the pinned, already-checked inode rather than resolving the
	// operator's pathname again. None of its components can be symlinks.
	readFD, openErr := syscall.Open("/proc/self/fd/"+strconv.Itoa(fd), syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if openErr != nil {
		if openErr == syscall.EACCES || openErr == syscall.EPERM {
			return nil, ErrDenied
		}
		return nil, ErrUnavailable
	}
	f := os.NewFile(uintptr(readFD), "protected secret file")
	defer f.Close()
	var actual syscall.Stat_t
	if syscall.Fstat(readFD, &actual) != nil || actual.Dev != st.Dev || actual.Ino != st.Ino || !safeFile(&actual, uid) {
		return nil, ErrDenied
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if canceled := contextError(ctx); canceled != nil {
		return nil, canceled
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	if len(data) > limit {
		return nil, ErrTooLarge
	}
	return data, nil
}

func openFilePath(ctx context.Context, ref FileRef, uid uint32) (int, error) {
	const flags = linuxOPath | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	fd, err := syscall.Open("/", flags|syscall.O_DIRECTORY, 0)
	if err != nil {
		return -1, ErrUnavailable
	}
	parts := strings.Split(ref.Path[1:], "/")
	for i, part := range parts {
		if err := contextError(ctx); err != nil {
			_ = syscall.Close(fd)
			return -1, err
		}
		var dir syscall.Stat_t
		if syscall.Fstat(fd, &dir) != nil || !safeDirectory(&dir, uid) {
			_ = syscall.Close(fd)
			return -1, ErrDenied
		}
		openFlags := flags
		if i != len(parts)-1 {
			openFlags |= syscall.O_DIRECTORY
		}
		next, err := syscall.Openat(fd, part, openFlags, 0)
		_ = syscall.Close(fd)
		if err != nil {
			switch err {
			case syscall.ENOENT:
				return -1, ErrNotFound
			case syscall.EACCES, syscall.EPERM, syscall.ELOOP, syscall.ENOTDIR:
				return -1, ErrDenied
			default:
				return -1, ErrUnavailable
			}
		}
		fd = next
	}
	return fd, nil
}

func safeDirectory(st *syscall.Stat_t, uid uint32) bool {
	// A root-owned sticky directory (e.g. /tmp) is acceptable: entries
	// must themselves be trusted, and other users cannot replace them.
	return st.Mode&syscall.S_IFMT == syscall.S_IFDIR && (st.Uid == 0 || st.Uid == uid) &&
		(st.Mode&0o022 == 0 || st.Uid == 0 && st.Mode&syscall.S_ISVTX != 0)
}

func safeFile(st *syscall.Stat_t, uid uint32) bool {
	mode := st.Mode & 0o7777
	return st.Mode&syscall.S_IFMT == syscall.S_IFREG && (st.Uid == 0 || st.Uid == uid) &&
		st.Nlink == 1 && (mode == 0o400 || mode == 0o600)
}
