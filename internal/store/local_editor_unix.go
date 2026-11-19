//go:build unix

package store

import (
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// LocalEditorWorkspace is deliberately separate from the root-file API. It
// exposes only existing nested modules of Brine-managed local stacks.
type LocalEditorWorkspace interface {
	LocalEditorFiles(name string) ([]string, error)
	ReadLocalEditorFile(name, filename string) (string, error)
	WriteLocalEditorFile(name, filename, content string) error
}

func nestedComponents(filename string) ([]string, error) {
	if strings.Contains(filename, `\`) || path.Clean(filename) != filename || strings.HasPrefix(filename, "/") || !strings.Contains(filename, "/") || !strings.HasSuffix(filename, ".pkl") {
		return nil, fmt.Errorf("invalid nested module path")
	}
	parts := strings.Split(filename, "/")
	if len(parts) > 16 {
		return nil, fmt.Errorf("nested module path too deep")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || part == ".git" || strings.ContainsRune(part, 0) {
			return nil, fmt.Errorf("invalid nested module path")
		}
	}
	return parts, nil
}

func (s *FSStore) localEditorParent(name, filename string) (int, string, error) {
	parts, err := nestedComponents(filename)
	if err != nil {
		return -1, "", err
	}
	stack, err := s.readMeta(name)
	if err != nil || stack.Git != nil || stack.RemoteGit != nil {
		return -1, "", fmt.Errorf("not a local workspace")
	}
	fd, err := s.workspaceFD(name, nil)
	if err != nil {
		return -1, "", err
	}
	for _, part := range parts[:len(parts)-1] {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return -1, "", e
		}
		fd = next
	}
	return fd, parts[len(parts)-1], nil
}

func (s *FSStore) ReadLocalEditorFile(name, filename string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fd, leaf, err := s.localEditorParent(name, filename)
	if err != nil {
		return "", err
	}
	defer unix.Close(fd)
	b, err := readWorkspaceFile(fd, leaf)
	return string(b), err
}
func (s *FSStore) WriteLocalEditorFile(name, filename, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fd, leaf, err := s.localEditorParent(name, filename)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	// Open without O_CREAT, and check the same regular/single-link invariants as root files.
	f, err := openWorkspaceFile(fd, leaf, unix.O_WRONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = unix.Fchmod(int(f.Fd()), 0600); err != nil {
		return err
	}
	if err = f.Truncate(0); err != nil {
		return err
	}
	if _, err = f.WriteString(content); err != nil {
		return err
	}
	stack, err := s.readMeta(name)
	if err != nil {
		return err
	}
	stack.UpdatedAt = time.Now()
	return s.writeMeta(stack)
}

func (s *FSStore) LocalEditorFiles(name string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stack, err := s.readMeta(name)
	if err != nil {
		return nil, err
	}
	if stack.Git != nil || stack.RemoteGit != nil {
		return nil, nil
	}
	fd, err := s.workspaceFD(name, nil)
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(fd), "workspace")
	defer root.Close()
	var files []string
	entries := 0
	var walk func(*os.File, string, int) error
	walk = func(dir *os.File, prefix string, depth int) error {
		for {
			es, e := dir.ReadDir(1)
			if e == io.EOF {
				return nil
			}
			if e != nil {
				return e
			}
			entries++
			if entries > 4096 {
				return fmt.Errorf("editor discovery entry limit exceeded")
			}
			item := es[0]
			n := item.Name()
			if n == ".git" {
				continue
			}
			p := n
			if prefix != "" {
				p = prefix + "/" + n
			}
			if item.IsDir() && depth < 15 {
				child, e := unix.Openat(int(dir.Fd()), n, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
				if e != nil {
					continue
				}
				f := os.NewFile(uintptr(child), p)
				e = walk(f, p, depth+1)
				f.Close()
				if e != nil {
					return e
				}
			} else if prefix != "" {
				if _, e := nestedComponents(p); e == nil {
					f, e := openWorkspaceFile(int(dir.Fd()), n, unix.O_RDONLY)
					if e == nil {
						f.Close()
						files = append(files, p)
					}
				}
			}
		}
	}
	if err := walk(root, "", 0); err != nil {
		return nil, err
	}
	return files, nil
}
