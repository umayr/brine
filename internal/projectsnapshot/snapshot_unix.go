//go:build linux || darwin

package projectsnapshot

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/sys/unix"
)

type walker struct {
	limits  Limits
	files   []File
	entries int
	total   int64
}

func build(root, entrypoint string, limits Limits) (Snapshot, error) {
	fd, err := unix.Open(filepath.Clean(root), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Snapshot{}, fmt.Errorf("open project root: %w", err)
	}
	dir := os.NewFile(uintptr(fd), "project root")
	defer dir.Close()
	w := walker{limits: limits}
	if err := w.walk(dir, "", 0); err != nil {
		return Snapshot{}, err
	}
	sort.Slice(w.files, func(i, j int) bool { return w.files[i].Path < w.files[j].Path })
	found := false
	for _, f := range w.files {
		if f.Path == entrypoint {
			found = true
			break
		}
	}
	if !found {
		return Snapshot{}, fmt.Errorf("entrypoint is not a captured regular file")
	}
	return Snapshot{Entrypoint: entrypoint, Files: w.files}, nil
}

func sameIdentity(a, b *unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode
}
func unchanged(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func (w *walker) walk(dir *os.File, prefix string, depth int) error {
	before, err := dir.Stat()
	if err != nil {
		return err
	}
	// Read one name at a time so an enormous directory cannot bypass MaxEntries
	// by allocating its entire listing before checking the bound.
	for {
		entries, err := dir.ReadDir(1)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read directory: %w", err)
		}
		for _, e := range entries {
			name := e.Name()
			if name == ".git" {
				continue
			}
			w.entries++
			if w.entries > w.limits.MaxEntries {
				return fmt.Errorf("entry limit exceeded")
			}
			if depth+1 > w.limits.MaxDepth {
				return fmt.Errorf("depth limit exceeded")
			}
			p := name
			if prefix != "" {
				p = prefix + "/" + name
			}
			var listed unix.Stat_t
			if err := unix.Fstatat(int(dir.Fd()), name, &listed, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return fmt.Errorf("inspect %q: %w", p, err)
			}
			kind := listed.Mode & unix.S_IFMT
			if kind != unix.S_IFREG && kind != unix.S_IFDIR {
				return fmt.Errorf("unsupported file type at %q", p)
			}
			flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
			if kind == unix.S_IFDIR {
				flags |= unix.O_DIRECTORY
			}
			fd, err := unix.Openat(int(dir.Fd()), name, flags, 0)
			if err != nil {
				return fmt.Errorf("open %q: %w", p, err)
			}
			f := os.NewFile(uintptr(fd), p)
			err = w.capture(dir, f, name, p, depth, &listed)
			closeErr := f.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
	after, err := dir.Stat()
	if err != nil {
		return err
	}
	if !unchanged(before, after) {
		return fmt.Errorf("directory changed during capture")
	}
	return nil
}

func (w *walker) capture(parent, f *os.File, name, p string, depth int, listed *unix.Stat_t) error {
	var opened unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &opened); err != nil {
		return err
	}
	if !sameIdentity(listed, &opened) {
		return fmt.Errorf("entry changed at %q", p)
	}
	before, err := f.Stat()
	if err != nil {
		return err
	}
	if before.IsDir() {
		if err := w.walk(f, p, depth+1); err != nil {
			return err
		}
	} else {
		if len(w.files) >= w.limits.MaxFiles {
			return fmt.Errorf("file limit exceeded")
		}
		if before.Size() < 0 || before.Size() > w.limits.MaxFileBytes || before.Size() > w.limits.MaxTotalBytes-w.total {
			return fmt.Errorf("byte limit exceeded at %q", p)
		}
		// Limit the read even if a concurrently growing file reports a smaller size.
		capBytes := w.limits.MaxFileBytes
		if remaining := w.limits.MaxTotalBytes - w.total; remaining < capBytes {
			capBytes = remaining
		}
		data, err := io.ReadAll(io.LimitReader(f, capBytes+1))
		if err != nil {
			return fmt.Errorf("read %q: %w", p, err)
		}
		if int64(len(data)) > capBytes || int64(len(data)) != before.Size() {
			return fmt.Errorf("file changed or byte limit exceeded at %q", p)
		}
		after, err := f.Stat()
		if err != nil {
			return err
		}
		if !unchanged(before, after) {
			return fmt.Errorf("file changed during capture at %q", p)
		}
		w.total += int64(len(data))
		w.files = append(w.files, File{Path: p, Content: data, SHA256: sha256.Sum256(data)})
	}
	var final unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &final, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("recheck %q: %w", p, err)
	}
	if !sameIdentity(&opened, &final) || opened.Size != final.Size {
		return fmt.Errorf("entry changed at %q", p)
	}
	return nil
}
