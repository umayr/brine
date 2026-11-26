package webhooks

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var ErrKey = errors.New("webhook encryption key unavailable or insecure")

// LoadKey never provisions or repairs key material. The key is exactly32 raw
// bytes in a private, single-link, owner-controlled regular file; not hex text.
func LoadKey(path string) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return nil, ErrKey
	}
	before, e := os.Lstat(path)
	if e != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || before.Mode().Perm()&0400 == 0 {
		return nil, ErrKey
	}
	st, ok := before.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || before.Size() != 32 {
		return nil, ErrKey
	}
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return nil, ErrKey
	}
	f := os.NewFile(uintptr(fd), "webhook-encryption-key")
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(before, opened) {
		return nil, ErrKey
	}
	key, e := io.ReadAll(io.LimitReader(f, 33))
	if e != nil || len(key) != 32 {
		clear(key)
		return nil, ErrKey
	}
	after, e := os.Lstat(path)
	if e != nil || !os.SameFile(before, after) || after.Size() != 32 || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		clear(key)
		return nil, ErrKey
	}
	return key, nil
}
