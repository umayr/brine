//go:build linux || darwin

package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

const HistoryFilename = "history.sqlite3"

// PersistentStore is an unmounted prototype. All callers must cooperate and
// keep the private directory and its trusted ancestors stable on a local FS.
type PersistentStore struct {
	mu     sync.RWMutex
	store  *Store
	dir    *os.File
	closed bool
}

// OpenPersistent explicitly opens a fixed-name store in an existing private
// directory. create only creates a new store, never adopts an existing file.
func OpenPersistent(dir string, create bool) (*PersistentStore, error) {
	return openPersistent(dir, create, persistentFormat{
		init:     []string{schema, "PRAGMA application_id = 1112690245", "PRAGMA user_version = 1"},
		validate: validatePersistent,
	})
}

type persistentFormat struct {
	init     []string
	validate func(*sql.DB) error
}

// The fixed name, ownership and no-adoption checks are shared across formats.
func openPersistent(dir string, create bool, format persistentFormat) (_ *PersistentStore, err error) {
	d, err := openHistoryDir(dir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			d.Close()
		}
	}()
	if err = unix.Flock(int(d.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, HistoryFilename)
	if err = noSidecars(path); err != nil {
		return nil, err
	}
	var original os.FileInfo
	if create {
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if e != nil {
			return nil, e
		}
		original, e = f.Stat()
		if e != nil {
			f.Close()
			return nil, e
		}
		if e = f.Close(); e != nil {
			return nil, e
		}
	} else {
		info, e := os.Lstat(path)
		if e != nil {
			return nil, e
		}
		if !persistentFile(info) {
			return nil, errors.New("unsafe database")
		}
		original = info
		ro, e := sql.Open("sqlite", sqliteURL(path, true))
		if e != nil {
			return nil, e
		}
		e = format.validate(ro)
		ce := ro.Close()
		if e != nil {
			return nil, e
		}
		if ce != nil {
			return nil, ce
		}
		after, e := os.Lstat(path)
		if e != nil {
			return nil, e
		}
		if !persistentFile(after) || !os.SameFile(info, after) {
			return nil, errors.New("database changed")
		}
	}
	if err = noSidecars(path); err != nil {
		return nil, err
	}
	if err = recheckPersistent(dir, path, d, original); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("mode", "rw")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	defer func() {
		if err != nil {
			db.Close()
		}
	}()
	if create {
		tx, e := db.Begin()
		if e != nil {
			return nil, e
		}
		for _, s := range format.init {
			if _, e = tx.Exec(s); e != nil {
				tx.Rollback()
				return nil, e
			}
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
	} else if err = format.validate(db); err != nil {
		return nil, err
	}
	return &PersistentStore{store: &Store{db: db}, dir: d}, nil
}
func recheckPersistent(dir, path string, held *os.File, original os.FileInfo) error {
	check, err := openHistoryDir(dir)
	if err != nil {
		return err
	}
	defer check.Close()
	a, e1 := held.Stat()
	b, e2 := check.Stat()
	if e1 != nil || e2 != nil || !os.SameFile(a, b) {
		return errors.New("directory changed")
	}
	now, err := os.Lstat(path)
	if err != nil || !persistentFile(now) || !persistentFile(original) || !os.SameFile(original, now) {
		return errors.New("database changed")
	}
	return noSidecars(path)
}
func persistentFile(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink == 1 && privateFile(info) && info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0
}
func validatePersistent(db *sql.DB) error {
	var id, version, count int
	var actual string
	if err := db.QueryRow("PRAGMA application_id").Scan(&id); err != nil {
		return err
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&count); err != nil {
		return err
	}
	if id != appID || version != 1 || count != 1 {
		return errors.New("incompatible database")
	}
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='observations'").Scan(&actual); err != nil || actual != schema {
		return errors.New("incompatible schema")
	}
	return nil
}
func (s *PersistentStore) Append(ctx context.Context, r Record) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return errors.New("closed")
	}
	return s.store.Append(ctx, r)
}
func (s *PersistentStore) Read(ctx context.Context, limit int) ([]Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, errors.New("closed")
	}
	return s.store.Read(ctx, limit)
}
func (s *PersistentStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	e := s.store.Close()
	e2 := s.dir.Close()
	if e != nil {
		return e
	}
	return e2
}
