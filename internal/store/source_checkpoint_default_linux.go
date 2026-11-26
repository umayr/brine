//go:build linux

package store

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

const sourceCheckpointDefaultFile = "source-checkpoint-default"

// Missing policy starts off; unreadable, unsafe or malformed policy never
// authorizes archive writes. A trusted root is required even with no file.
func readSourceCheckpointDefault(root int) (bool, error) {
	var st unix.Stat_t
	if unix.Fstat(root, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&07777 != 0700 || int(st.Uid) != unix.Geteuid() {
		return false, ErrSourceUnavailable
	}
	for _, attr := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		if _, err := unix.Fgetxattr(root, attr, nil); err != unix.ENODATA {
			return false, ErrSourceUnavailable
		}
	}
	if err := unix.Fstatat(root, sourceCheckpointDefaultFile, &st, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return false, nil
	} else if err != nil {
		return false, ErrSourceUnavailable
	}
	data, err := readSourceFile(root, sourceCheckpointDefaultFile, 4)
	if err != nil {
		return false, ErrSourceUnavailable
	}
	switch string(data) {
	case "on\n":
		return true, nil
	case "off\n":
		return false, nil
	default:
		return false, ErrSourceUnavailable
	}
}

func (s *FSStore) SourceCheckpointDefault() (bool, error) {
	root, err := unix.Open(s.dataDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, ErrSourceUnavailable
	}
	defer unix.Close(root)
	return readSourceCheckpointDefault(root)
}

func (s *FSStore) SetSourceCheckpointDefault(ctx context.Context, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.lockManualRoot() // Shared with automatic publication admission.
	if err != nil {
		return ErrSourceUnavailable
	}
	defer unix.Close(root)
	if _, err = readSourceCheckpointDefault(root); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	value := "off\n"
	if enabled {
		value = "on\n"
	}
	return replaceWorkspaceMetadata(root, sourceCheckpointDefaultFile, []byte(value))
}
