//go:build unix

package store

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Workspace components are opened relative to trusted directory descriptors;
// repositories may contain symlinks, but editor paths must never follow them.
func (s *FSStore) stackFD(name string) (int, error) {
	if ValidateStackName(name) != nil {
		return -1, fmt.Errorf("invalid stack")
	}
	root, err := unix.Open(s.dataDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	defer unix.Close(root)
	stacks, err := unix.Openat(root, "stacks", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	defer unix.Close(stacks)
	return unix.Openat(stacks, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}
func (s *FSStore) workspaceFD(name string, git *GitSource) (int, error) {
	fd, err := s.stackFD(name)
	if err != nil {
		return -1, err
	}
	if git == nil {
		return fd, nil
	}
	if validateFolder(git.Folder) != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("invalid folder")
	}
	parts := []string{"repo"}
	if git.Folder != "" {
		parts = append(parts, strings.Split(filepath.Clean(git.Folder), string(filepath.Separator))...)
	}
	for _, p := range parts {
		next, e := unix.Openat(fd, p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return -1, e
		}
		fd = next
	}
	return fd, nil
}
func openWorkspaceFile(dir int, name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(dir, name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		unix.Close(fd)
		return nil, fmt.Errorf("unsafe workspace file")
	}
	return os.NewFile(uintptr(fd), name), nil
}
func readWorkspaceFile(dir int, name string) ([]byte, error) {
	f, err := openWorkspaceFile(dir, name, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
func writeWorkspaceFile(dir int, name string, data []byte) error {
	f, err := openWorkspaceFile(dir, name, unix.O_WRONLY|unix.O_CREAT)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = unix.Fchmod(int(f.Fd()), 0600); err != nil {
		return err
	}
	if err = unix.Ftruncate(int(f.Fd()), 0); err != nil {
		return err
	}
	_, err = f.Write(data)
	return err
}
