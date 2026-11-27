package webhooks

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

var errStorage = errors.New("webhook storage unavailable")

type Service struct {
	db                *sql.DB
	lock              *os.File
	crypt             cipher.AEAD
	client            *http.Client
	cfg               Config
	now               func() time.Time
	running           atomic.Bool
	lifecycle         sync.Mutex
	cancel            context.CancelFunc
	done              chan struct{}
	incomingRunning   bool
	incomingCancel    context.CancelFunc
	incomingDone      chan struct{}
	incomingAdmission chan struct{}
	incomingWaiters   chan struct{}
	metadataObserve   MetadataObservationFunc
	sample            func(context.Context) error
	samplingStatus    atomic.Int32
	closed            bool
	closeOnce         sync.Once
	closeErr          error
}

func newID(prefix string) string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic("secure randomness unavailable")
	}
	return prefix + hex.EncodeToString(b)
}
func (s *Service) seal(clear []byte) ([]byte, error) {
	n := make([]byte, s.crypt.NonceSize())
	if _, e := rand.Read(n); e != nil {
		return nil, errStorage
	}
	return s.crypt.Seal(n, n, clear, []byte("brine-webhooks-v1")), nil
}
func (s *Service) unseal(b []byte) ([]byte, error) {
	n := s.crypt.NonceSize()
	if len(b) < n {
		return nil, errStorage
	}
	out, e := s.crypt.Open(nil, b[:n], b[n:], []byte("brine-webhooks-v1"))
	if e != nil {
		return nil, errStorage
	}
	return out, nil
}
func marshal(v any) []byte         { b, _ := json.Marshal(v); return b }
func encodeTime(t time.Time) int64 { return t.UnixMilli() }

// Open requires an exclusively controlled private directory and an externally
// provisioned encryption key. Wrong keys fail closed before workers can run.
func Open(cfg Config) (*Service, error) {
	if len(cfg.EncryptionKey) != 32 || cfg.Directory == "" {
		return nil, ErrInvalid
	}
	client, e := NewDeliveryClient(cfg.AllowPrivateCIDRs)
	if e != nil {
		return nil, e
	}
	if cfg.MaxPending == 0 {
		cfg.MaxPending = 10000
	}
	if cfg.MaxPending < 1 || cfg.MaxPending > 100000 {
		return nil, ErrInvalid
	}
	if cfg.Retention == 0 {
		cfg.Retention = 7 * 24 * time.Hour
	}
	if cfg.Retention < 24*time.Hour || cfg.Retention > 90*24*time.Hour {
		return nil, ErrInvalid
	}
	dir, e := filepath.Abs(cfg.Directory)
	if e != nil {
		return nil, ErrInvalid
	}
	if e = os.MkdirAll(dir, 0700); e != nil {
		return nil, errStorage
	}
	info, e := os.Lstat(dir)
	if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errStorage
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, errStorage
	}
	// The owner-only directory prevents other users racing pathname checks.
	for _, name := range []string{"webhooks.lock", "webhooks.db", "webhooks.db-wal", "webhooks.db-shm", "webhooks.db-journal"} {
		p := filepath.Join(dir, name)
		st, err := os.Lstat(p)
		if err == nil {
			stat, ok := st.Sys().(*syscall.Stat_t)
			if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
				return nil, errStorage
			}
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, errStorage
		}
	}
	_, lockErr := os.Lstat(filepath.Join(dir, "webhooks.lock"))
	dbInfo, dbErr := os.Lstat(filepath.Join(dir, "webhooks.db"))
	if os.IsNotExist(lockErr) != os.IsNotExist(dbErr) || (dbErr == nil && dbInfo.Size() == 0) {
		return nil, errStorage
	}
	lock, e := os.OpenFile(filepath.Join(dir, "webhooks.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, errStorage
	}
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		lock.Close()
		return nil, errStorage
	}
	fail := func() { syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); lock.Close() }
	path := filepath.Join(dir, "webhooks.db")
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		fail()
		return nil, errStorage
	}
	f.Close()
	u := url.URL{Scheme: "file", Path: path}
	db, e := sql.Open("sqlite", u.String())
	if e != nil {
		fail()
		return nil, errStorage
	}
	db.SetMaxOpenConns(1)
	block, _ := aes.NewCipher(cfg.EncryptionKey)
	crypt, _ := cipher.NewGCM(block)
	cfg.EncryptionKey = nil // Do not retain an additional cleartext copy.
	s := &Service{db: db, lock: lock, crypt: crypt, client: client, cfg: cfg, now: func() time.Time { return time.Now().UTC() }, incomingAdmission: make(chan struct{}, 1), incomingWaiters: make(chan struct{}, 64)}
	if _, e = db.Exec(schema + triggerSchema + operationSchema + metadataSchema + observationSchema); e != nil {
		s.Close()
		return nil, errStorage
	}
	var verifier []byte
	e = db.QueryRow("SELECT value FROM metadata WHERE key='key-check'").Scan(&verifier)
	if errors.Is(e, sql.ErrNoRows) {
		var populated int
		if db.QueryRow("SELECT (SELECT COUNT(*) FROM endpoints)+(SELECT COUNT(*) FROM events)+(SELECT COUNT(*) FROM triggers)+(SELECT COUNT(*) FROM incoming_requests)+(SELECT COUNT(*) FROM lifecycle_records)+(SELECT COUNT(*) FROM metadata_mutations)").Scan(&populated) != nil || populated != 0 {
			s.Close()
			return nil, errStorage
		}
		verifier, e = s.seal([]byte("brine-webhooks-key-v1"))
		if e == nil {
			_, e = db.Exec("INSERT INTO metadata(key,value) VALUES('key-check',?)", verifier)
		}
	} else if e == nil {
		var clear []byte
		clear, e = s.unseal(verifier)
		if e == nil && string(clear) != "brine-webhooks-key-v1" {
			e = errStorage
		}
	}
	if e != nil {
		s.Close()
		return nil, errStorage
	}
	// The exclusive process lock proves a previous delivery lease is abandoned.
	if e = s.recoverClaims(context.Background()); e != nil {
		s.Close()
		return nil, errStorage
	}
	if e = s.recoverOperations(context.Background()); e != nil {
		s.Close()
		return nil, e
	}
	if e = s.recoverIncoming(context.Background()); e != nil {
		s.Close()
		return nil, e
	}
	return s, nil
}

// recoverClaims charges abandoned sends against their already reserved attempt.
func (s *Service) recoverClaims(ctx context.Context) error {
	_, e := s.db.ExecContext(ctx, `UPDATE deliveries SET status=CASE
 WHEN NOT EXISTS(SELECT 1 FROM endpoints ep WHERE ep.id=deliveries.endpoint_id AND json_extract(CAST(ep.config AS TEXT),'$.url')=deliveries.url AND ep.secret=deliveries.secret) THEN 'cancelled'
 WHEN attempts>=8 OR created<? THEN 'exhausted' ELSE 'pending' END,
 error_category='delivery_unconfirmed',updated=? WHERE status='sending'`, encodeTime(s.now().Add(-24*time.Hour)), encodeTime(s.now()))
	if e != nil {
		return errStorage
	}
	return nil
}

func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.lifecycle.Lock()
		s.closed = true
		if s.cancel != nil {
			s.cancel()
		}
		done := s.done
		if s.incomingCancel != nil {
			s.incomingCancel()
		}
		incomingDone := s.incomingDone
		s.lifecycle.Unlock()
		if done != nil {
			<-done
		}
		if incomingDone != nil {
			<-incomingDone
		}
		s.client.CloseIdleConnections()
		s.closeErr = s.db.Close()
		syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		s.lock.Close()
	})
	return s.closeErr
}

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA synchronous=FULL;
PRAGMA foreign_keys=ON;
PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS metadata(key TEXT PRIMARY KEY,value BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS endpoints(id TEXT PRIMARY KEY, config BLOB NOT NULL, secret BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS events(id TEXT PRIMARY KEY, payload BLOB NOT NULL, created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS deliveries(
 id TEXT PRIMARY KEY, endpoint_id TEXT NOT NULL, event_id TEXT NOT NULL REFERENCES events(id),
 url TEXT NOT NULL, secret BLOB NOT NULL, status TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0,
 http_status INTEGER NOT NULL DEFAULT 0, error_category TEXT NOT NULL DEFAULT '',
 next_at INTEGER NOT NULL, created INTEGER NOT NULL, updated INTEGER NOT NULL,
 UNIQUE(endpoint_id,event_id));
CREATE INDEX IF NOT EXISTS delivery_due ON deliveries(status,next_at);
CREATE TABLE IF NOT EXISTS attempts(delivery_id TEXT NOT NULL REFERENCES deliveries(id) ON DELETE CASCADE,
 number INTEGER NOT NULL, at INTEGER NOT NULL, duration INTEGER NOT NULL, http_status INTEGER NOT NULL,
 error_category TEXT NOT NULL, PRIMARY KEY(delivery_id,number));
`

func (s *Service) Endpoints(ctx context.Context) ([]Endpoint, error) {
	rows, e := s.db.QueryContext(ctx, "SELECT config FROM endpoints ORDER BY id LIMIT 100")
	if e != nil {
		return nil, errStorage
	}
	defer rows.Close()
	out := []Endpoint{}
	for rows.Next() {
		var b []byte
		var ep Endpoint
		if rows.Scan(&b) != nil || json.Unmarshal(b, &ep) != nil {
			return nil, errStorage
		}
		out = append(out, ep)
	}
	if rows.Err() != nil {
		return nil, errStorage
	}
	return out, nil
}

func (s *Service) endpoint(ctx context.Context, id string) (Endpoint, []byte, error) {
	var ep Endpoint
	var b, secret []byte
	e := s.db.QueryRowContext(ctx, "SELECT config,secret FROM endpoints WHERE id=?", id).Scan(&b, &secret)
	if errors.Is(e, sql.ErrNoRows) {
		return ep, nil, ErrNotFound
	}
	if e != nil || json.Unmarshal(b, &ep) != nil {
		return ep, nil, errStorage
	}
	return ep, secret, nil
}

func (s *Service) prune(ctx context.Context) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return errStorage
	}
	defer tx.Rollback()
	cutoff := encodeTime(s.now().Add(-s.cfg.Retention))
	if _, e = tx.ExecContext(ctx, "DELETE FROM deliveries WHERE updated<? AND status IN ('delivered','exhausted','cancelled')", cutoff); e != nil {
		return errStorage
	}
	// Dedupe is guaranteed for the configured journal retention window.
	if _, e = tx.ExecContext(ctx, "DELETE FROM events WHERE created<? AND NOT EXISTS(SELECT 1 FROM deliveries WHERE event_id=events.id)", cutoff); e != nil {
		return errStorage
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM observations WHERE updated<?", cutoff); e != nil {
		return errStorage
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM metadata_mutations WHERE state IN ('published','aborted') AND updated<?", cutoff); e != nil {
		return errStorage
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM lifecycle_records WHERE terminal_sent=1 AND updated<?", cutoff); e != nil {
		return errStorage
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM incoming_requests WHERE updated<? AND status NOT IN ('accepted','running','unknown','awaiting_review')", cutoff); e != nil {
		return errStorage
	}
	if e = tx.Commit(); e != nil {
		return errStorage
	}
	return nil
}
