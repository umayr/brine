//go:build linux

package config

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Read-only startup preflight, not host attestation or a retained state handle.
// Dill independently revalidates its state for every qualified invocation.
func validateCoordinationState(path string) error {
	unsafe := fmt.Errorf("coordination state must be preexisting, canonical, operator-owned mode 0700 with no extended/default ACLs and safe ancestors")
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return unsafe
	}
	defer func() { unix.Close(fd) }()
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			next, e := unix.Openat(fd, parts[i], unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if e != nil {
				return unsafe
			}
			unix.Close(fd)
			fd = next
		}
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || st.Mode&0022 != 0 {
			return unsafe
		}
		if i == len(parts)-1 && (st.Uid != uint32(os.Geteuid()) || st.Mode&07777 != 0700) {
			return unsafe
		}
		for _, name := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
			if _, e := unix.Fgetxattr(fd, name, nil); e != unix.ENODATA {
				return unsafe
			}
		}
	}
	return nil
}
