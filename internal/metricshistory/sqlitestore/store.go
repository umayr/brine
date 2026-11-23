// Package sqlitestore is an unmounted, temporary-file-only SQLite proof.
package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `CREATE TABLE observations (receipt_ns INTEGER NOT NULL, target TEXT NOT NULL, gauge TEXT NOT NULL, value INTEGER NOT NULL)`
const appID = 0x42524e45

// Record is a known, observed integer gauge, including zero. Receipt is the
// Brine observation time, not a source sample time.
type Record struct {
	Receipt time.Time
	Target  string
	Gauge   string
	Value   int64
}
type Store struct{ db *sql.DB }

// Open explicitly opens a proof database beneath the process temporary directory.
// create must be true only for a new path; existing files are never initialized.
// The caller must exclusively control the directory and database for its lifetime.
func Open(path string, create bool) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("absolute temporary path required")
	}
	root, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return nil, err
	}
	parentInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if !parentInfo.IsDir() || parentInfo.Mode().Perm() != 0700 || parentInfo.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || !owned(parentInfo) {
		return nil, errors.New("private owner-only directory required")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, parent)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("path must be in temporary directory")
	}
	path = filepath.Join(parent, filepath.Base(path))
	if err := noSidecars(path); err != nil {
		return nil, err
	}
	if create {
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if e != nil {
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
		if !privateFile(info) {
			return nil, errors.New("private regular file required")
		}
		// Immutable admission avoids journal recovery or any SQLite write to an unknown file.
		ro, e := sql.Open("sqlite", sqliteURL(path, true))
		if e != nil {
			return nil, e
		}
		e = validate(ro)
		closeErr := ro.Close()
		if e != nil {
			return nil, e
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if e = noSidecars(path); e != nil {
			return nil, e
		}
		after, e := os.Lstat(path)
		if e != nil {
			return nil, e
		}
		if !privateFile(after) || !os.SameFile(info, after) {
			return nil, errors.New("file changed during admission")
		}
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "rw")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*Store, error) { db.Close(); return nil, e }
	ctx := context.Background()
	if create {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return fail(e)
		}
		for _, statement := range []string{schema, "PRAGMA application_id = 1112690245", "PRAGMA user_version = 1"} {
			if _, e = tx.ExecContext(ctx, statement); e != nil {
				tx.Rollback()
				return fail(e)
			}
		}
		if e = tx.Commit(); e != nil {
			return fail(e)
		}
	} else if err = validate(db); err != nil {
		return fail(err)
	}
	return &Store{db: db}, nil
}

func owned(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint64(st.Uid) == uint64(os.Geteuid())
}
func privateFile(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm()&0077 == 0 && owned(info)
}
func noSidecars(path string) error {
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		_, err := os.Lstat(path + suffix)
		if err == nil {
			return errors.New("sidecar exists")
		}
		if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
func sqliteURL(path string, immutable bool) string {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "ro")
	if immutable {
		q.Set("immutable", "1")
	}
	u.RawQuery = q.Encode()
	return u.String()
}
func validate(db *sql.DB) error {
	var id, version, count int
	var actual string
	if err := db.QueryRow("PRAGMA application_id").Scan(&id); err != nil {
		return err
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
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

// Append inserts a single known observation atomically; it never replaces data.
func (s *Store) Append(ctx context.Context, r Record) error {
	if !r.Receipt.After(time.Unix(0, 0)) || r.Receipt.After(time.Unix(0, 1<<63-1)) || r.Target == "" || len(r.Target) > 256 || (r.Gauge != "memory_bytes" && r.Gauge != "count") || r.Value < 0 || r.Value > 1<<53-1 {
		return errors.New("invalid observation")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO observations VALUES (?, ?, ?, ?)", r.Receipt.UnixNano(), r.Target, r.Gauge, r.Value); err != nil {
		return err
	}
	return tx.Commit()
}

// Read returns at most limit observations in insertion order.
func (s *Store) Read(ctx context.Context, limit int) ([]Record, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("limit outside 1..1000")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT receipt_ns,target,gauge,value FROM observations ORDER BY rowid LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		var ns int64
		if err = rows.Scan(&ns, &r.Target, &r.Gauge, &r.Value); err != nil {
			return nil, err
		}
		r.Receipt = time.Unix(0, ns).UTC()
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return out, nil
}
func (s *Store) Close() error { return s.db.Close() }
