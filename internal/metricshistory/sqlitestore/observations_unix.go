//go:build linux || darwin

package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/umayr/brine/internal/agentmetrics"
)

// EnrollmentID is a Brine-assigned opaque incarnation, not a target name,
// signing key, or an ID supplied by a provider. The registry owner must assign
// a fresh ID on enrollment and must not reuse a forgotten ID.
type EnrollmentID [16]byte

// Observation records one independently qualified capture and Brine's receipt
// time. The caller still owns authorization, freshness, and enrollment checks.
// Received is not the source sample time.
type Observation struct {
	Received time.Time
	Sample   agentmetrics.Sample
}

const (
	observationAppID = 0x42524d48 // BRMH; distinct from the gauge proof.
	maxMetric        = 1<<53 - 1
	maxHistoryRows   = 1000
)

// A single row preserves the atomic measurement and field presence. One sample
// per enrollment and receipt nanosecond; collisions reject, never overwrite.
const observationSchema = `CREATE TABLE captures (
 enrollment BLOB NOT NULL CHECK(length(enrollment) = 16),
 received_ns INTEGER NOT NULL CHECK(received_ns > 0),
 host_total INTEGER CHECK(host_total BETWEEN 1 AND 9007199254740991 AND host_total % 1024 = 0),
 host_available INTEGER CHECK(host_available BETWEEN 0 AND host_total AND host_available % 1024 = 0),
 owned_count INTEGER CHECK(owned_count BETWEEN 0 AND 9007199254740991),
 owned_usage INTEGER CHECK(owned_usage BETWEEN 0 AND 9007199254740991),
 CHECK((host_total IS NULL) = (host_available IS NULL)),
 CHECK(owned_usage IS NULL OR (owned_count IS NOT NULL AND owned_count > 0)),
 CHECK(host_total IS NOT NULL OR owned_usage IS NOT NULL),
 PRIMARY KEY (enrollment, received_ns)
) STRICT, WITHOUT ROWID`

var observationFormat = persistentFormat{
	init:     []string{observationSchema, "PRAGMA application_id = 0x42524d48", "PRAGMA user_version = 1"},
	validate: validateObservations,
}

// ObservationStore owns a private store lock until Close. It exposes neither
// the prototype gauge API nor unscoped queries.
type ObservationStore struct {
	root      *PersistentStore
	maxPages  int
	writer    sync.Mutex
	suspended bool // protected by writer; never automatically reopened
}

// InitializeObservations explicitly provisions an empty versioned store. An
// existing DB, including the prototype database, is not adopted or modified.
func InitializeObservations(dir string) error {
	p, err := openPersistent(dir, true, observationFormat)
	if err != nil {
		return err
	}
	return p.Close()
}

// OpenObservations requires a previously initialized store. It never creates,
// migrates or recovers a SQLite file as a side effect of startup.
func OpenObservations(dir string) (*ObservationStore, error) {
	p, err := openPersistent(dir, false, observationFormat)
	if err != nil {
		return nil, err
	}
	return &ObservationStore{root: p}, nil
}

func validateObservations(db *sql.DB) error {
	var app, version, count int
	var definition string
	if err := db.QueryRow("PRAGMA main.application_id").Scan(&app); err != nil {
		return err
	}
	if err := db.QueryRow("PRAGMA main.user_version").Scan(&version); err != nil {
		return err
	}
	if err := db.QueryRow("SELECT count(*) FROM main.sqlite_schema").Scan(&count); err != nil {
		return err
	}
	if app != observationAppID || version != 1 || count != 1 {
		return errors.New("incompatible observation store")
	}
	if err := db.QueryRow("SELECT sql FROM main.sqlite_schema WHERE type='table' AND name='captures' AND tbl_name='captures'").Scan(&definition); err != nil || definition != observationSchema {
		return errors.New("incompatible observation schema")
	}
	return nil
}

func receiptNS(when time.Time) (int64, bool) {
	if !when.After(time.Unix(0, 0)) || when.After(time.Unix(0, 1<<63-1)) {
		return 0, false
	}
	return when.UnixNano(), true
}

func validSample(s agentmetrics.Sample) bool {
	if !s.HasHost && (s.HostTotal != 0 || s.HostAvailable != 0) ||
		!s.HasOwnedCount && s.OwnedCount != 0 || !s.HasOwnedUsage && s.OwnedUsage != 0 {
		return false
	}
	if s.HasHost && (s.HostTotal == 0 || s.HostTotal > maxMetric || s.HostAvailable > s.HostTotal || s.HostAvailable%1024 != 0 || s.HostTotal%1024 != 0) {
		return false
	}
	if s.OwnedCount > maxMetric || s.OwnedUsage > maxMetric || s.HasOwnedUsage && (!s.HasOwnedCount || s.OwnedCount == 0) {
		return false
	}
	return s.HasHost || s.HasOwnedUsage
}

func nullable(value uint64, known bool) any {
	if !known {
		return nil
	}
	return int64(value)
}

// Append writes one observation atomically. It does not upsert or retain
// unknown fields as zeros. Storage errors do not permit retry or deletion here.
func (s *ObservationStore) Append(ctx context.Context, id EnrollmentID, o Observation) error {
	ns, ok := receiptNS(o.Received)
	if !ok || id == (EnrollmentID{}) || !validSample(o.Sample) {
		return errors.New("invalid observation")
	}
	p := s.root
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return errors.New("observation store closed")
	}
	v := o.Sample
	exec := p.store.db.ExecContext
	if s.maxPages != 0 {
		s.writer.Lock()
		defer s.writer.Unlock()
		if s.suspended {
			return ErrObservationWritesSuspended
		}
		// Configure on the connection that runs the statement. A pool
		// replacement must not silently lose a connection-local cap.
		conn, err := p.store.db.Conn(ctx)
		if err != nil {
			return s.recordWriteError(err)
		}
		defer conn.Close()
		if err := configureBoundedConn(ctx, conn, s.maxPages); err != nil {
			return s.recordWriteError(err)
		}
		exec = conn.ExecContext
	}
	_, err := exec(ctx, `INSERT INTO captures
	 (enrollment, received_ns, host_total, host_available, owned_count, owned_usage) VALUES (?, ?, ?, ?, ?, ?)`,
		id[:], ns, nullable(v.HostTotal, v.HasHost), nullable(v.HostAvailable, v.HasHost),
		nullable(v.OwnedCount, v.HasOwnedCount), nullable(v.OwnedUsage, v.HasOwnedUsage))
	if s.maxPages != 0 {
		return s.recordWriteError(err)
	}
	return err
}

// Read returns at most limit observations for one enrollment over an inclusive
// time interval, newest first. It never merges different enrollments.
func (s *ObservationStore) Read(ctx context.Context, id EnrollmentID, from, through time.Time, limit int) ([]Observation, error) {
	lo, lok := receiptNS(from)
	hi, hok := receiptNS(through)
	if id == (EnrollmentID{}) || !lok || !hok || lo > hi || limit < 1 || limit > maxHistoryRows {
		return nil, errors.New("invalid observation range")
	}
	p := s.root
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return nil, errors.New("observation store closed")
	}
	rows, err := p.store.db.QueryContext(ctx, `SELECT received_ns, host_total, host_available, owned_count, owned_usage
	 FROM captures WHERE enrollment = ? AND received_ns BETWEEN ? AND ? ORDER BY received_ns DESC LIMIT ?`, id[:], lo, hi, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []Observation
	for rows.Next() {
		var ns int64
		var total, avail, count, usage sql.NullInt64
		if err := rows.Scan(&ns, &total, &avail, &count, &usage); err != nil {
			return nil, err
		}
		if total.Valid != avail.Valid || total.Int64 < 0 || avail.Int64 < 0 || count.Int64 < 0 || usage.Int64 < 0 {
			return nil, errors.New("invalid stored observation")
		}
		sample := agentmetrics.Sample{
			HasHost: total.Valid, HostTotal: uint64(total.Int64), HostAvailable: uint64(avail.Int64),
			HasOwnedCount: count.Valid, OwnedCount: uint64(count.Int64), HasOwnedUsage: usage.Valid, OwnedUsage: uint64(usage.Int64),
		}
		if !validSample(sample) {
			return nil, errors.New("invalid stored observation")
		}
		all = append(all, Observation{Received: time.Unix(0, ns).UTC(), Sample: sample})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return all, nil
}

func (s *ObservationStore) Close() error { return s.root.Close() }
