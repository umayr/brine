package store

import (
	"bytes"
	"encoding/json"
	"golang.org/x/sys/unix"
	"os"
)

// MetadataObserver journals the registration commit, never source bytes. It is
// configured once before serving. Callbacks run under the store mutation lock
// and MUST NOT call back into StackStore or change execution authorization.
type MetadataChange struct {
	Before, After *Stack
	Kind          string
}
type MetadataCommit struct {
	ID   string
	Done func(error)
}
type MetadataObserver func(MetadataChange) (MetadataCommit, error)

func (s *FSStore) SetMetadataObserver(observer MetadataObserver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metadataObserver = observer
}

func (s *FSStore) prepareMetadata(after *Stack, kind string, name string) (MetadataCommit, error) {
	noop := MetadataCommit{Done: func(error) {}}
	if s.metadataObserver == nil {
		return noop, nil
	}
	var before *Stack
	fd, e := s.stackFD(name)
	if e == nil {
		_, metaErr := readWorkspaceFile(fd, "meta.json")
		unix.Close(fd)
		if metaErr == nil {
			before, e = s.readMeta(name)
			if e != nil {
				return noop, e
			}
		} else if !os.IsNotExist(metaErr) {
			return noop, metaErr
		}
	} else if !os.IsNotExist(e) {
		return noop, e
	}
	if before != nil && after != nil {
		a, b := *before, *after
		a.EventHead = ""
		b.EventHead = ""
		if bytes.Equal(metadataJSON(&a), metadataJSON(&b)) {
			after.EventHead = before.EventHead
			return noop, nil
		}
	}
	commit, e := s.metadataObserver(MetadataChange{Before: before, After: after, Kind: kind})
	if e != nil {
		return noop, e
	}
	if commit.Done == nil {
		commit.Done = func(error) {}
	}
	if after != nil {
		after.EventHead = commit.ID
	}
	return commit, nil
}

// ObserveMetadata distinguishes a missing registration from a missing global
// inventory root. Only the former is authoritative evidence of deletion.
func (s *FSStore) ObserveMetadata(name string) (*Stack, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if e := ValidateStackName(name); e != nil {
		return nil, false, e
	}
	root, e := s.stacksFD()
	if e != nil {
		return nil, false, e
	}
	defer unix.Close(root)
	var stat unix.Stat_t
	e = unix.Fstatat(root, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if os.IsNotExist(e) {
		if e = unix.Fsync(root); e != nil {
			return nil, false, e
		}
		return nil, false, nil
	}
	if e != nil {
		return nil, false, e
	}
	st, e := s.readMeta(name)
	if e != nil {
		return nil, false, e
	}
	child, e := s.stackFD(name)
	if e != nil {
		return nil, false, e
	}
	defer unix.Close(child)
	if e = unix.Fsync(child); e != nil {
		return nil, false, e
	}
	if e = unix.Fsync(root); e != nil {
		return nil, false, e
	}
	return st, true, nil
}

func (s *FSStore) syncInventory() error {
	fd, e := s.stacksFD()
	if e != nil {
		return e
	}
	defer unix.Close(fd)
	return unix.Fsync(fd)
}

func metadataJSON(st *Stack) []byte { b, _ := json.Marshal(st); return b }
