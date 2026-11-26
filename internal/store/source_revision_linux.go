//go:build linux

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Keep descriptor-relative reads on the same mount, including bind mounts on
// the same device. No approximation on kernels/filesystems without openat2.
func openSourceAt(parent int, name string, directory bool) (*os.File, unix.Stat_t, error) {
	var st unix.Stat_t
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat2(parent, name, &unix.OpenHow{Flags: uint64(flags), Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	if err != nil {
		return nil, st, ErrSourceUnavailable
	}
	f := os.NewFile(uintptr(fd), name)
	for _, attr := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		if _, err := unix.Fgetxattr(fd, attr, nil); err != unix.ENODATA {
			f.Close()
			return nil, st, ErrSourceUnavailable
		}
	}
	typ, perm := uint32(unix.S_IFREG), uint32(0600)
	if directory {
		typ, perm = unix.S_IFDIR, 0700
	}
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != typ || st.Mode&07777 != perm || st.Uid != uint32(os.Geteuid()) || (!directory && st.Nlink != 1) {
		f.Close()
		return nil, st, ErrSourceUnavailable
	}
	return f, st, nil
}
func readSourceFile(dir int, name string, limit int) ([]byte, error) {
	f, before, e := openSourceAt(dir, name, false)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	if before.Size > int64(limit) {
		return nil, ErrSourceUnavailable
	}
	b, e := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	var after unix.Stat_t
	if e != nil || int64(len(b)) != before.Size || len(b) > limit || unix.Fstat(int(f.Fd()), &after) != nil || after.Ctim != before.Ctim {
		return nil, ErrSourceUnavailable
	}
	return b, nil
}
func sourceState(ctx context.Context, stacks int, name string) (SourceView, *Stack, error) {
	return sourceStateAt(ctx, stacks, name, name)
}
func sourceStateAt(ctx context.Context, stacks int, dir, name string) (SourceView, *Stack, error) {
	var v SourceView
	root, identity, e := openSourceAt(stacks, dir, true)
	if e != nil {
		return v, nil, e
	}
	defer root.Close()
	raw, e := readSourceFile(int(root.Fd()), "meta.json", 64<<10)
	if e != nil {
		return v, nil, e
	}
	var st Stack
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&st) != nil || d.Decode(new(any)) != io.EOF || st.Name != name || st.Git != nil || st.RemoteGit != nil || st.CreatedAt.IsZero() || st.UpdatedAt.IsZero() || !st.SourceHistory.valid() {
		return v, nil, ErrSourceUnavailable
	}
	// Source publication accepts only the registration format written by
	// Brine. Duplicate, aliased or omitted fields are ambiguous authority.
	canonical, err := json.Marshal(&st)
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(canonical, compact.Bytes()) {
		return v, nil, ErrSourceUnavailable
	}
	v.Files = make(map[string][]byte)
	size, entries := 0, 0
	var walk func(*os.File, string) error
	walk = func(dir *os.File, prefix string) error {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			es, err := dir.ReadDir(1)
			if err == io.EOF {
				break
			}
			if err != nil {
				return ErrSourceUnavailable
			}
			item := es[0]
			entries++
			if entries > 256 {
				return ErrSourceUnavailable
			}
			p := prefix + item.Name()
			if p == "meta.json" {
				continue
			}
			if !sourcePathOK(p, item.IsDir()) {
				return ErrSourceUnavailable
			}
			if item.IsDir() {
				sub, _, err := openSourceAt(int(dir.Fd()), item.Name(), true)
				if err != nil {
					return err
				}
				n := len(v.Files)
				err = walk(sub, p+"/")
				sub.Close()
				if err != nil {
					return err
				}
				if len(v.Files) == n {
					return ErrSourceUnavailable
				}
				continue
			}
			if len(v.Files) >= 128 {
				return ErrSourceUnavailable
			}
			b, err := readSourceFile(int(dir.Fd()), item.Name(), maxSourceSize-size)
			if err != nil {
				return err
			}
			v.Files[p] = b
			size += len(b)
		}
		return nil
	}
	if e = walk(root, ""); e != nil {
		return v, nil, e
	}
	v.Entrypoint = st.EffectiveEntrypoint()
	if !validSourceFiles(v.Files, v.Entrypoint) {
		return v, nil, ErrSourceUnavailable
	}
	if st.SourceHistory != nil {
		v.Scope = revisionScope(&st, st.SourceHistory.Identity)
		v.Revisions = slices.Clone(st.SourceHistory.Revisions)
	}
	v.Baseline = sourceBaseline(&st, fmt.Sprintf("%d/%d", identity.Dev, identity.Ino), v.Files)
	return v, &st, nil
}
func (s *FSStore) ReviewSource(ctx context.Context, name string) (SourceView, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.sourceInterrupted(name); err != nil {
		return SourceView{}, err
	}
	fd, e := s.stacksFD()
	if e != nil {
		return SourceView{}, ErrSourceUnavailable
	}
	defer unix.Close(fd)
	v, _, e := sourceState(ctx, fd, name)
	return v, e
}

// Caller holds deployment admission for the review/commit. The store mutex
// excludes local editor/API writers; the root flock excludes cross-process
// ledger transactions from the admission check through workspace publication.
// External concurrent workspace writers are unsupported.
func (s *FSStore) CommitSource(ctx context.Context, name, baseline string, build SourceChange) error {
	return s.commitSource(ctx, name, baseline, false, build)
}
func (s *FSStore) CommitAutomaticCheckpoint(ctx context.Context, name, baseline string, build SourceChange) error {
	return s.commitSource(ctx, name, baseline, true, build)
}

func (s *FSStore) commitSource(ctx context.Context, name, baseline string, automatic bool, build SourceChange) (resultErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.lockManualRoot()
	if err != nil {
		return ErrSourceUnavailable
	}
	defer unix.Close(root)
	rows, err := existingManualIntentsLocked(root)
	if err != nil {
		return ErrSourceUnavailable
	}
	for _, r := range rows {
		if r.Stack == name && !r.Resolved {
			return ErrSourceUnavailable
		}
	}
	if err := s.sourceInterrupted(name); err != nil {
		return err
	}
	fd, e := s.stacksFD()
	if e != nil {
		return ErrSourceUnavailable
	}
	defer unix.Close(fd)
	v, st, e := sourceState(ctx, fd, name)
	if e != nil {
		return e
	}
	if v.Baseline != baseline {
		return ErrSourceStale
	}
	if automatic {
		var enabled bool
		if st.AutoCheckpoint != nil {
			enabled = *st.AutoCheckpoint
		} else {
			enabled, e = readSourceCheckpointDefault(root)
			if e != nil {
				return e
			}
		}
		if !enabled {
			return ErrSourceCheckpointDisabled
		}
	}
	lineage := st.SourceHistory
	if lineage == nil {
		lineage, e = newLineage()
		if e != nil {
			return e
		}
		v.Scope = revisionScope(st, lineage.Identity)
	}
	next, e := build(v)
	if e != nil {
		return e
	}
	if len(next.NewRevisions) > 2 || (len(next.NewRevisions) == 0 && (!next.SetAutoCheckpoint || next.Files != nil)) ||
		(next.SetAutoCheckpoint && next.Files != nil) || (automatic && (next.SetAutoCheckpoint || next.Files != nil || len(next.NewRevisions) != 1)) {
		return ErrSourceUnavailable
	}
	if len(next.NewRevisions) > 0 {
		lineage.Revisions = append(lineage.Revisions, next.NewRevisions...)
		st.SourceHistory = lineage
	}
	if next.SetAutoCheckpoint {
		st.AutoCheckpoint = next.AutoCheckpoint
	}
	if !st.SourceHistory.valid() || (st.AutoCheckpoint != nil && *st.AutoCheckpoint && st.SourceHistory == nil) {
		return ErrSourceUnavailable
	}
	// Nothing is published in the workspace until the exact object(s) are durable.
	verify := func() error {
		check, _, err := sourceState(ctx, fd, name)
		if err != nil {
			return err
		}
		if check.Baseline != baseline {
			return ErrSourceStale
		}
		return ctx.Err()
	}
	if e = verify(); e != nil {
		return e
	}
	if next.Files == nil {
		if e = s.writeMeta(st); e != nil {
			return ErrSourceUncertain
		}
		return nil
	}
	if !validSourceFiles(next.Files, next.Entrypoint) {
		return ErrSourceUnavailable
	}
	st.Entrypoint = next.Entrypoint
	st.PklPath = filepath.Join(s.stackDir(name), next.Entrypoint)
	st.UpdatedAt = time.Now().UTC()
	commit, observeErr := s.prepareMetadata(st, "restore", name)
	if observeErr != nil {
		return observeErr
	}
	defer func() { commit.Done(resultErr) }()
	// Both directories are private siblings. A leftover stage is an explicit
	// durable fence (also checked when the optional archive is no longer open).
	stage := stageName(name)
	if e = unix.Mkdirat(fd, stage, 0700); e != nil {
		return ErrSourceInterrupted
	}
	if e = unix.Fsync(fd); e != nil {
		return ErrSourceInterrupted
	}
	stagePath := filepath.Join(s.dataDir, "stacks", stage)
	if e = stageSourceTree(stagePath, st, next.Files); e != nil {
		return ErrSourceInterrupted
	}
	staged, meta, err := sourceStateAt(ctx, fd, stage, name)
	if err != nil || sourceBaseline(st, "", next.Files) != sourceBaseline(meta, "", staged.Files) {
		return ErrSourceInterrupted
	}
	if e = verify(); e != nil {
		if removeReviewedTree(stagePath, next.Files) != nil || unix.Fsync(fd) != nil {
			return ErrSourceInterrupted
		}
		return e
	}
	if e = unix.Renameat2(fd, stage, fd, name, unix.RENAME_EXCHANGE); e != nil {
		return ErrSourceInterrupted
	}
	if e = unix.Fsync(fd); e != nil {
		return ErrSourceUncertain
	}
	// Only remove the reviewed, previously retained source tree, never an
	// unknown or mounted subtree. stage is not a valid Brine stack registration.
	old, _, e := sourceStateAt(context.Background(), fd, stage, name)
	if e != nil || old.Baseline != baseline {
		return ErrSourceUncertain
	}
	if e = removeReviewedTree(stagePath, v.Files); e != nil {
		return ErrSourceUncertain
	}
	if e = unix.Fsync(fd); e != nil {
		return ErrSourceUncertain
	}
	return nil
}
func stageSourceTree(dir string, st *Stack, files map[string][]byte) error {
	root, e := os.OpenRoot(dir)
	if e != nil {
		return e
	}
	defer root.Close()
	dirs := map[string]bool{".": true}
	put := func(p string, b []byte) error {
		f, e := root.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = f.Write(b)
		return errors.Join(e, f.Sync(), f.Close())
	}
	for p, b := range files {
		d := filepath.Dir(p)
		if e = root.MkdirAll(d, 0700); e != nil {
			return e
		}
		for d != "." {
			dirs[d] = true
			d = filepath.Dir(d)
		}
		if e = put(p, b); e != nil {
			return e
		}
	}
	meta, e := json.MarshalIndent(st, "", "  ")
	if e != nil {
		return e
	}
	if e = put("meta.json", meta); e != nil {
		return e
	}
	for d := range dirs {
		f, e := root.Open(d)
		if e != nil {
			return e
		}
		if e = errors.Join(f.Sync(), f.Close()); e != nil {
			return e
		}
	}
	return nil
}

// Unlike recursive removal, unlink exactly the known files/dirs. Unexpected
// entries keep the fence in place. Never descend into an unreviewed directory.
func removeReviewedTree(dir string, files map[string][]byte) error {
	root, e := os.OpenRoot(dir)
	if e != nil {
		return e
	}
	defer root.Close()
	dirs := map[string]bool{}
	for p := range files {
		if e = root.Remove(p); e != nil {
			return e
		}
		for d := filepath.Dir(p); d != "."; d = filepath.Dir(d) {
			dirs[d] = true
		}
	}
	if e = root.Remove("meta.json"); e != nil {
		return e
	}
	ordered := make([]string, 0, len(dirs))
	for d := range dirs {
		ordered = append(ordered, d)
	}
	slices.SortFunc(ordered, func(a, b string) int { return strings.Compare(b, a) })
	for _, d := range ordered {
		if e = root.Remove(d); e != nil {
			return e
		}
	}
	return os.Remove(dir)
}
