//go:build unix

package store

import (
	"errors"
	"golang.org/x/sys/unix"
)

func (s *FSStore) stacksFD() (int, error) {
	root, err := unix.Open(s.dataDir, unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	defer unix.Close(root)
	return unix.Openat(root, "stacks", unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
}
func stageName(name string) string { return ".source-restore-" + name }
func (s *FSStore) sourceInterrupted(name string) error {
	if err := ValidateStackName(name); err != nil {
		return err
	}
	fd, err := s.stacksFD()
	if err != nil {
		return ErrSourceUnavailable
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err = unix.Fstatat(fd, stageName(name), &st, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil
	}
	return ErrSourceInterrupted
}
