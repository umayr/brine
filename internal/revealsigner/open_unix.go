//go:build linux || darwin

package revealsigner

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// Open loads an existing, strictly encoded, private seed file. It never creates a key.
func Open(path string) (*Signer, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return nil, ErrKey
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrKey
	}
	f := os.NewFile(uintptr(fd), "reveal signing key")
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !safe(before) {
		return nil, ErrKey
	}
	data, err := io.ReadAll(io.LimitReader(f, 66))
	if err != nil || (len(data) != 64 && (len(data) != 65 || data[64] != '\n')) {
		return nil, ErrKey
	}
	for _, c := range data[:64] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return nil, ErrKey
		}
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := hex.Decode(seed, data[:64]); err != nil {
		return nil, ErrKey
	}
	// Best-effort detection is not a stable snapshot or a defense against any
	// concurrent writer or path controller, including ACL-authorized actors.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, ErrKey
	}
	again, err := io.ReadAll(io.LimitReader(f, 66))
	if err != nil || !bytes.Equal(data, again) {
		return nil, ErrKey
	}
	after, err := f.Stat()
	if err != nil || !safe(after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, ErrKey
	}
	named, err := os.Lstat(path)
	if err != nil || !safe(named) || !os.SameFile(after, named) {
		return nil, ErrKey
	}
	return &Signer{private: ed25519.NewKeyFromSeed(seed)}, nil
}

func safe(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && st.Mode&07777 == 0600 && st.Uid == uint32(os.Geteuid())
}
