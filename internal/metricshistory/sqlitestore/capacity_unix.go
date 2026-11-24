//go:build linux || darwin

package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var (
	ErrObservationCapacity        = errors.New("metrics history SQLite capacity reached")
	ErrObservationWritesSuspended = errors.New("metrics history writes suspended until restart")
)

// OpenBoundedObservations opens an existing, provisioned rollback-mode
// observation database. The cap limits main-file pages only, not the journal
// or filesystem usage. Writable product history also needs a quota boundary.
func OpenBoundedObservations(dir string, maxPages int) (*ObservationStore, error) {
	if maxPages < 2 || uint64(maxPages) > 0xfffffffe {
		return nil, errors.New("invalid observation page limit")
	}
	format := observationFormat
	format.checkExisting = checkRollbackHeader
	format.validate = func(db *sql.DB) error {
		if err := validateObservations(db); err != nil {
			return err
		}
		var pages int
		if err := db.QueryRow("PRAGMA main.page_count").Scan(&pages); err != nil {
			return err
		}
		if pages > maxPages {
			return ErrObservationCapacity
		}
		return nil
	}
	root, err := openPersistent(dir, false, format)
	if err != nil {
		return nil, err
	}
	return &ObservationStore{root: root, maxPages: maxPages}, nil
}

// Immutable SQLite probes treat a WAL-mode header as rollback mode; check
// its version bytes directly before opening any SQLite connection.
func checkRollbackHeader(path string, original os.FileInfo) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !persistentFile(info) || !os.SameFile(original, info) {
		return errors.New("observation database changed")
	}
	var header [20]byte
	if _, err := f.ReadAt(header[:], 0); err != nil || string(header[:16]) != "SQLite format 3\x00" || header[18] != 1 || header[19] != 1 {
		return errors.New("observation database is not rollback format")
	}
	return nil
}

func configureBoundedConn(ctx context.Context, conn *sql.Conn, maxPages int) error {
	var mode string
	if err := conn.QueryRowContext(ctx, "PRAGMA main.journal_mode").Scan(&mode); err != nil {
		return err
	}
	if mode != "delete" {
		return errors.New("unexpected observation journal mode")
	}
	for _, q := range []string{"PRAGMA synchronous=FULL", "PRAGMA temp_store=MEMORY", "PRAGMA busy_timeout=0"} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	var actual int
	if err := conn.QueryRowContext(ctx, fmt.Sprintf("PRAGMA main.max_page_count=%d", maxPages)).Scan(&actual); err != nil {
		return err
	}
	if actual != maxPages {
		return ErrObservationCapacity
	}
	return nil
}

// The caller holds writer. An ordinary cancellation or single-row constraint
// does not permit replay and does not mark the store as a failed device.
func (s *ObservationStore) recordWriteError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		switch sqliteErr.Code() & 0xff {
		case sqlite3.SQLITE_CONSTRAINT:
			return err
		case sqlite3.SQLITE_FULL:
			err = ErrObservationCapacity
		}
	}
	s.suspended = true
	return err
}
