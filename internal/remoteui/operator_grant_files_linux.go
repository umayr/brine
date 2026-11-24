//go:build linux

package remoteui

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/targetregistry"
	"golang.org/x/sys/unix"
)

// privateOperatorGrantSource is unmounted. It copies exact, separately
// approved metadata at construction. A file cannot create, change or renew an
// approval. Its basename is the approved opaque reference, never a form path.
// Replacing/revoking the source belongs to the future custody owner. Do not
// mount a source without a lifecycle that invalidates previously held clients.
type privateOperatorGrantSource struct {
	root     string
	approved operatorGrantApproval
}

func (*privateOperatorGrantSource) String() string { return "[private operator grant source redacted]" }
func (*privateOperatorGrantSource) GoString() string {
	return "[private operator grant source redacted]"
}
func (*privateOperatorGrantSource) MarshalJSON() ([]byte, error) { return nil, errOperatorAuthority }

func newPrivateOperatorGrantSource(root string, approved operatorGrantApproval) (*privateOperatorGrantSource, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" || len(root) > 4096 || strings.ContainsRune(root, 0) ||
		approved.Enrollment == (targetregistry.HistoryIncarnation{}) || !attendedHex(approved.Reference, 32) || !attendedHex(approved.Revision, 32) ||
		approved.LocalGrants != agentclient.LocalNewAttended || approved.TokenCommitment == ([32]byte{}) {
		return nil, errOperatorAuthority
	}
	return &privateOperatorGrantSource{root: root, approved: approved}, nil
}

func (s *privateOperatorGrantSource) LoadCurrent(ctx context.Context, active targetregistry.HistoryIncarnation) (operatorGrantApproval, string, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || active != s.approved.Enrollment || active == (targetregistry.HistoryIncarnation{}) {
		return operatorGrantApproval{}, "", errOperatorAuthority
	}
	dir, err := openPrivateOperatorRoot(ctx, s.root)
	if err != nil {
		return operatorGrantApproval{}, "", errOperatorAuthority
	}
	token, err := readOperatorBearer(ctx, dir, s.approved.Reference, s.approved.TokenCommitment)
	closeErr := unix.Close(dir)
	if err != nil || closeErr != nil || ctx.Err() != nil {
		return operatorGrantApproval{}, "", errOperatorAuthority
	}
	return s.approved, token, nil
}

func operatorACLFree(fd int, directory bool) bool {
	if _, err := unix.Fgetxattr(fd, "system.posix_acl_access", nil); err != unix.ENODATA {
		return false
	}
	if directory {
		if _, err := unix.Fgetxattr(fd, "system.posix_acl_default", nil); err != unix.ENODATA {
			return false
		}
	}
	return true
}

func trustedOperatorDirectory(fd int, final bool) bool {
	var st unix.Stat_t
	uid := uint32(os.Geteuid())
	return unix.Fstat(fd, &st) == nil && st.Mode&unix.S_IFMT == unix.S_IFDIR && st.Mode&0022 == 0 && (st.Uid == 0 || st.Uid == uid) &&
		(!final || (st.Uid == uid && st.Mode&07777 == 0700)) && operatorACLFree(fd, true)
}

func openPrivateOperatorRoot(ctx context.Context, path string) (int, error) {
	const flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Open("/", flags, 0)
	if err != nil {
		return -1, errOperatorAuthority
	}
	if !trustedOperatorDirectory(fd, false) {
		_ = unix.Close(fd)
		return -1, errOperatorAuthority
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, name := range parts {
		if ctx.Err() != nil {
			_ = unix.Close(fd)
			return -1, errOperatorAuthority
		}
		next, err := unix.Openat(fd, name, flags, 0)
		closeErr := unix.Close(fd)
		if err != nil {
			return -1, errOperatorAuthority
		}
		if closeErr != nil || !trustedOperatorDirectory(next, i == len(parts)-1) {
			_ = unix.Close(next)
			return -1, errOperatorAuthority
		}
		fd = next
	}
	return fd, nil
}

func validOperatorBearerStat(st *unix.Stat_t) bool {
	return st.Mode&unix.S_IFMT == unix.S_IFREG && st.Mode&07777 == 0600 && st.Uid == uint32(os.Geteuid()) && st.Nlink == 1 && (st.Size == 43 || st.Size == 44)
}

func readOperatorBearer(ctx context.Context, dir int, name string, commitment [32]byte) (token string, retErr error) {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", errOperatorAuthority
	}
	f := os.NewFile(uintptr(fd), "private operator grant")
	defer func() {
		if err := f.Close(); err != nil {
			token, retErr = "", errOperatorAuthority
		}
	}()
	var before, after, entry unix.Stat_t
	if unix.Fstat(fd, &before) != nil || !validOperatorBearerStat(&before) || !operatorACLFree(fd, false) || ctx.Err() != nil {
		return "", errOperatorAuthority
	}
	var raw [45]byte
	defer clear(raw[:])
	n, err := io.ReadFull(f, raw[:])
	if err != io.ErrUnexpectedEOF || int64(n) != before.Size || unix.Fstat(fd, &after) != nil || !validOperatorBearerStat(&after) || !operatorACLFree(fd, false) ||
		unix.Fstatat(dir, name, &entry, unix.AT_SYMLINK_NOFOLLOW) != nil || !validOperatorBearerStat(&entry) ||
		!sameOperatorBearerFile(before, after) || !sameOperatorBearerFile(after, entry) || ctx.Err() != nil || (n == 44 && raw[43] != '\n') {
		return "", errOperatorAuthority
	}
	codec := base64.RawURLEncoding.Strict()
	var decoded [32]byte
	defer clear(decoded[:])
	count, err := codec.Decode(decoded[:], raw[:43])
	if err != nil || count != len(decoded) {
		return "", errOperatorAuthority
	}
	got := sha256.Sum256(raw[:43])
	if subtle.ConstantTimeCompare(got[:], commitment[:]) != 1 {
		return "", errOperatorAuthority
	}
	return string(raw[:43]), nil
}

func sameOperatorBearerFile(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Size == b.Size && a.Ctim == b.Ctim && a.Mtim == b.Mtim
}
