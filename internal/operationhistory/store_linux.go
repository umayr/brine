//go:build linux

package operationhistory

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

type linuxBackend struct {
	root *os.File
	path string
}

func aclFree(fd int) bool {
	for _, name := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		if _, e := unix.Fgetxattr(fd, name, nil); e != unix.ENODATA {
			return false
		}
	}
	return true
}
func directory(fd int, private bool) bool {
	var st unix.Stat_t
	return unix.Fstat(fd, &st) == nil && st.Mode&unix.S_IFMT == unix.S_IFDIR && (st.Uid == 0 || st.Uid == uint32(os.Geteuid())) && st.Mode&0022 == 0 && (!private || st.Uid == uint32(os.Geteuid()) && st.Mode&07777 == 0700) && aclFree(fd)
}
func regular(fd int) bool {
	var st unix.Stat_t
	return unix.Fstat(fd, &st) == nil && st.Mode&unix.S_IFMT == unix.S_IFREG && st.Mode&07777 == 0600 && st.Uid == uint32(os.Geteuid()) && st.Nlink == 1 && st.Size <= MaxRecord && aclFree(fd)
}
func openRoot(path string) (int, error) {
	fd, e := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return -1, ErrUnsafe
	}
	if !directory(fd, false) {
		unix.Close(fd)
		return -1, ErrUnsafe
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, p := range parts {
		if p == "" || p == "." || p == ".." {
			unix.Close(fd)
			return -1, ErrUnsafe
		}
		next, e := unix.Openat(fd, p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return -1, ErrUnsafe
		}
		fd = next
		if !directory(fd, i == len(parts)-1) {
			unix.Close(fd)
			return -1, ErrUnsafe
		}
	}
	return fd, nil
}
func openBackend(path string) (backend, error) {
	fd, e := openRoot(path)
	if e != nil {
		return nil, e
	}
	// Lock the retained directory inode: no lock-file path/repair race. The lock
	// survives rename and is released only on server shutdown/process death.
	if unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
		unix.Close(fd)
		return nil, ErrUnsafe
	}
	return &linuxBackend{root: os.NewFile(uintptr(fd), "operation-history"), path: path}, nil
}
func (b *linuxBackend) check() error {
	fd, e := openRoot(b.path)
	if e != nil {
		return ErrUnsafe
	}
	defer unix.Close(fd)
	var held, current unix.Stat_t
	if unix.Fstat(int(b.root.Fd()), &held) != nil || unix.Fstat(fd, &current) != nil || held.Dev != current.Dev || held.Ino != current.Ino {
		return ErrUnsafe
	}
	return nil
}
func (b *linuxBackend) close() error { return b.root.Close() }
func (b *linuxBackend) names() ([]string, error) {
	if b.check() != nil {
		return nil, ErrUnsafe
	}
	fd, e := unix.Openat(int(b.root.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, ErrUnsafe
	}
	f := os.NewFile(uintptr(fd), "operation-history")
	defer f.Close()
	names, e := f.Readdirnames(MaxRecords + 1)
	if e != nil && !errors.Is(e, io.EOF) {
		return nil, ErrUnsafe
	}
	return names, nil
}
func (b *linuxBackend) read(name string) ([]byte, error) {
	if b.check() != nil {
		return nil, ErrUnsafe
	}
	fd, e := unix.Openat(int(b.root.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if e != nil {
		if e == unix.ENOENT {
			return nil, os.ErrNotExist
		}
		return nil, ErrUnsafe
	}
	f := os.NewFile(uintptr(fd), "operation-record")
	defer f.Close()
	if !regular(fd) {
		return nil, ErrUnsafe
	}
	data, e := io.ReadAll(io.LimitReader(f, MaxRecord+1))
	if e != nil || len(data) > MaxRecord || !regular(fd) {
		return nil, ErrUnsafe
	}
	return data, nil
}
func (b *linuxBackend) write(name string, data []byte) error {
	if len(data) > MaxRecord || b.check() != nil {
		return ErrUnsafe
	}
	if _, e := b.read(name); e != nil && !errors.Is(e, os.ErrNotExist) {
		return ErrUnsafe
	}
	var random [16]byte
	if _, e := rand.Read(random[:]); e != nil {
		return ErrUnsafe
	}
	temp := ".pending-" + hex.EncodeToString(random[:])
	fd, e := unix.Openat(int(b.root.Fd()), temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		return ErrUnsafe
	}
	f := os.NewFile(uintptr(fd), "operation-record")
	// No chmod, repair, unlink or cleanup of uncertain state. A failed publication
	// may leave a private pending file; restart then fails closed for inspection.
	if !regular(fd) {
		f.Close()
		return ErrUnsafe
	}
	n, e := f.Write(data)
	if e != nil || n != len(data) {
		f.Close()
		return ErrUnsafe
	}
	if f.Sync() != nil {
		f.Close()
		return ErrUnsafe
	}
	if f.Close() != nil {
		return ErrUnsafe
	}
	if b.check() != nil || unix.Renameat(int(b.root.Fd()), temp, int(b.root.Fd()), name) != nil || unix.Fsync(int(b.root.Fd())) != nil {
		return ErrUnsafe
	}
	return nil
}
