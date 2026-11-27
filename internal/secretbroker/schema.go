package secretbroker

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	_ "modernc.org/sqlite"
)

const (
	schemaVersion = 2
	applicationID = 0x4252424b
)

// The revision row is both snapshot metadata and its audit event. A single
// transaction inserts it and every reference row; later writes never update it.
// This unpublished v2 adds immutable broker intent/outcome records; old state is
// refused, never migrated/reset implicitly.
var tables = []struct {
	name string
	ddl  string
}{
	{"broker_meta", `CREATE TABLE broker_meta (
		singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
		identity TEXT NOT NULL CHECK (length(identity) = 32)
	) STRICT`},
	{"broker_registrations", `CREATE TABLE broker_registrations (
		id TEXT PRIMARY KEY CHECK (length(id) = 32),
		stack_name TEXT NOT NULL CHECK (length(stack_name) BETWEEN 1 AND 128),
		target_id TEXT NOT NULL CHECK (length(target_id) BETWEEN 1 AND 128)
	) STRICT`},
	{"broker_revisions", `CREATE TABLE broker_revisions (
		registration_id TEXT NOT NULL REFERENCES broker_registrations(id),
		revision INTEGER NOT NULL CHECK (revision BETWEEN 0 AND 127),
		actor TEXT NOT NULL CHECK (length(actor) BETWEEN 1 AND 128),
		at_ns INTEGER NOT NULL CHECK (at_ns > 0),
		binding_count INTEGER NOT NULL CHECK (binding_count BETWEEN 0 AND 128),
		CHECK (revision <> 0 OR binding_count = 0),
		PRIMARY KEY (registration_id, revision)
	) STRICT`},
	{"broker_bindings", `CREATE TABLE broker_bindings (
		registration_id TEXT NOT NULL,
		revision INTEGER NOT NULL CHECK (revision > 0),
		name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 128),
		connection_id TEXT NOT NULL CHECK (length(connection_id) BETWEEN 1 AND 128),
		kind TEXT NOT NULL,
		vault_path TEXT NOT NULL CHECK (length(vault_path) <= 512),
		vault_property TEXT NOT NULL CHECK (length(vault_property) <= 128),
		vault_version INTEGER NOT NULL CHECK (vault_version >= 0),
		infisical_folder TEXT NOT NULL CHECK (length(infisical_folder) <= 512),
		infisical_key TEXT NOT NULL CHECK (length(infisical_key) <= 128),
		infisical_version INTEGER NOT NULL CHECK (infisical_version >= 0),
		local_alias TEXT NOT NULL CHECK (length(local_alias) <= 128),
		CHECK (
			(kind = 'vault' AND vault_path <> '' AND vault_property <> '' AND infisical_folder = '' AND infisical_key = '' AND infisical_version = 0 AND local_alias = '') OR
			(kind = 'infisical' AND infisical_folder <> '' AND infisical_key <> '' AND vault_path = '' AND vault_property = '' AND vault_version = 0 AND local_alias = '') OR
			(kind = 'local' AND local_alias <> '' AND vault_path = '' AND vault_property = '' AND vault_version = 0 AND infisical_folder = '' AND infisical_key = '' AND infisical_version = 0)
		),
		PRIMARY KEY (registration_id, revision, name),
		FOREIGN KEY (registration_id, revision) REFERENCES broker_revisions(registration_id, revision)
	) STRICT`},
	{"broker_deployments", `CREATE TABLE broker_deployments (id TEXT PRIMARY KEY CHECK(length(id)=32), registration_id TEXT NOT NULL REFERENCES broker_registrations(id), payload TEXT NOT NULL CHECK(length(payload)<=65536), at_ns INTEGER NOT NULL CHECK(at_ns>0)) STRICT`},
	{"broker_deployment_events", `CREATE TABLE broker_deployment_events (id TEXT NOT NULL REFERENCES broker_deployments(id), sequence INTEGER NOT NULL CHECK(sequence BETWEEN 0 AND 31), state TEXT NOT NULL, effects TEXT NOT NULL, operation_id TEXT NOT NULL, at_ns INTEGER NOT NULL CHECK(at_ns>0), PRIMARY KEY(id,sequence)) STRICT`},
}

// Provision initializes a fresh file in an operator-provisioned directory. It
// never opens existing state for initialization. If initialization fails after
// reserving the path, the file is left for the operator to inspect; a retry
// cannot overwrite it. Only call this for new state, never as Open recovery.
func Provision(path string) error {
	dir, err := privateParent(path)
	if err != nil {
		return err
	}
	if err := checkSidecars(path, false); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return ErrExists
	}
	if err != nil {
		return ErrUnavailable
	}
	defer file.Close()
	if file.Chmod(0600) != nil {
		return ErrUnavailable
	}
	info, err := file.Stat()
	if err != nil || !privateFile(info) {
		return ErrUnavailable
	}
	files := stateFiles{dir: dir, db: info}
	if err := files.check(path, false); err != nil {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ErrUnavailable
	}
	id := hex.EncodeToString(random[:])
	db, err := connect(path)
	if err != nil {
		return err
	}
	initErr := initialize(db, id)
	if initErr == nil {
		verifiedID, err := verify(db)
		if err != nil || verifiedID != id {
			initErr = ErrUnavailable
		}
	}
	closeErr := db.Close()
	if initErr != nil || closeErr != nil {
		return ErrUnavailable
	}
	if err := files.check(path, true); err != nil {
		return err
	}
	if file.Sync() != nil {
		return ErrUnavailable
	}
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return ErrUnavailable
	}
	defer parent.Close()
	opened, err := parent.Stat()
	if err != nil || !os.SameFile(dir, opened) || parent.Sync() != nil {
		return ErrUnavailable
	}
	return nil
}

// Open only opens a provisioned store. It refuses absent, unsafe, corrupted,
// or incompatible state instead of implicitly creating or migrating it.
func Open(path string) (*Store, error) {
	files, err := inspectFiles(path, true)
	if err != nil {
		return nil, err
	}
	db, err := connect(path)
	if err != nil {
		return nil, err
	}
	id, err := verify(db)
	if err == nil {
		err = files.check(path, true)
	}
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{state: &storeState{db: db, path: path, files: files, identity: id}}, nil
}

// SQLite operates by path, including rollback journal access. The private
// directory and its ancestry must remain under operator control while in use.
func privateParent(path string) (os.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Dir(path) == path {
		return nil, ErrUnsafePath
	}
	parent := filepath.Dir(path)
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil || canonical != parent {
		return nil, ErrUnsafePath
	}
	for ancestor := parent; ; ancestor = filepath.Dir(ancestor) {
		entry, e := os.Lstat(ancestor)
		if e != nil || !entry.IsDir() {
			return nil, ErrUnsafePath
		}
		st, ok := entry.Sys().(*syscall.Stat_t)
		if !ok || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || (entry.Mode().Perm()&0022 != 0 && !(st.Uid == 0 && entry.Mode()&os.ModeSticky != 0)) {
			return nil, ErrUnsafePath
		}
		if ancestor == filepath.Dir(ancestor) {
			break
		}
	}
	info, err := os.Lstat(parent)
	if err != nil || info.Mode() != os.ModeDir|0700 || !owned(info, false) {
		return nil, ErrUnsafePath
	}
	return info, nil
}

func owned(info os.FileInfo, singleLink bool) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid()) && (!singleLink || st.Nlink == 1)
}

func privateFile(info os.FileInfo) bool { return info.Mode() == 0600 && owned(info, true) }

func checkSidecars(path string, allowJournal bool) error {
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		info, err := os.Lstat(path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !allowJournal || suffix != "-journal" || !privateFile(info) {
			return ErrUnsafePath
		}
	}
	return nil
}

type stateFiles struct {
	dir, db os.FileInfo
}

func inspectFiles(path string, existing bool) (stateFiles, error) {
	dir, err := privateParent(path)
	if err != nil {
		return stateFiles{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return stateFiles{}, ErrUnavailable
	}
	if !privateFile(info) {
		return stateFiles{}, ErrUnsafePath
	}
	if existing && info.Size() < 100 { // SQLite database header length.
		return stateFiles{}, ErrUnavailable
	}
	if err := checkSidecars(path, existing); err != nil {
		return stateFiles{}, err
	}
	return stateFiles{dir: dir, db: info}, nil
}

func (f stateFiles) check(path string, existing bool) error {
	current, err := inspectFiles(path, existing)
	if err != nil {
		return err
	}
	if !os.SameFile(f.dir, current.dir) || !os.SameFile(f.db, current.db) {
		return ErrUnavailable
	}
	return nil
}

func connect(path string) (*sql.DB, error) {
	q := url.Values{
		"mode":    {"rw"}, // Never let the driver create a missing file.
		"_txlock": {"immediate"},
		"_pragma": {"busy_timeout(5000)", "foreign_keys(ON)", "synchronous(FULL)", "trusted_schema(OFF)"},
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, ErrUnavailable
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

func initialize(db *sql.DB, id string) error {
	tx, err := db.Begin()
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	for _, table := range tables {
		if _, err := tx.Exec(table.ddl); err != nil {
			return ErrUnavailable
		}
	}
	for _, pragma := range []string{
		fmt.Sprintf("PRAGMA application_id = %d", applicationID),
		fmt.Sprintf("PRAGMA user_version = %d", schemaVersion),
	} {
		if _, err := tx.Exec(pragma); err != nil {
			return ErrUnavailable
		}
	}
	if _, err := tx.Exec("INSERT INTO broker_meta (singleton, identity) VALUES (1, ?)", id); err != nil {
		return ErrUnavailable
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}

func readIdentity(tx *sql.Tx) (string, error) {
	var id string
	if err := tx.QueryRow("SELECT identity FROM broker_meta WHERE singleton = 1").Scan(&id); err != nil || !validID(id) {
		return "", ErrUnavailable
	}
	return id, nil
}

func verify(db *sql.DB) (string, error) {
	tx, err := db.Begin()
	if err != nil {
		return "", ErrUnavailable
	}
	defer tx.Rollback()
	for _, check := range []struct {
		query string
		want  int
	}{
		{"PRAGMA user_version", schemaVersion}, {"PRAGMA application_id", applicationID},
		{"PRAGMA foreign_keys", 1}, {"PRAGMA synchronous", 2},
	} {
		var n int
		if tx.QueryRow(check.query).Scan(&n) != nil || n != check.want {
			return "", ErrUnavailable
		}
	}
	for _, check := range []struct{ query, want string }{
		{"PRAGMA journal_mode", "delete"}, {"PRAGMA integrity_check(1)", "ok"},
	} {
		var s string
		if tx.QueryRow(check.query).Scan(&s) != nil || s != check.want {
			return "", ErrUnavailable
		}
	}
	if err := verifyTables(tx); err != nil {
		return "", err
	}
	id, err := readIdentity(tx)
	if err != nil || verifyState(tx) != nil || tx.Commit() != nil {
		return "", ErrUnavailable
	}
	return id, nil
}

func verifyTables(tx *sql.Tx) error {
	want := make(map[string]string, len(tables))
	for _, table := range tables {
		want[table.name] = table.ddl
	}
	rows, err := tx.Query(`SELECT name, type, sql FROM sqlite_schema WHERE sql IS NOT NULL OR name NOT GLOB 'sqlite_autoindex_*'`)
	if err != nil {
		return ErrUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		var name, kind, ddl string
		if rows.Scan(&name, &kind, &ddl) != nil || kind != "table" || want[name] != ddl || ddl == "" {
			return ErrUnavailable
		}
		delete(want, name)
	}
	if rows.Err() != nil || len(want) != 0 {
		return ErrUnavailable
	}
	return nil
}

func verifyState(tx *sql.Tx) error {
	checks := []string{
		"PRAGMA foreign_key_check",
		`SELECT 1 FROM broker_registrations AS s LEFT JOIN broker_revisions AS r ON s.id = r.registration_id
		 GROUP BY s.id HAVING count(r.revision) = 0 OR min(r.revision) <> 0 OR max(r.revision) <> count(r.revision) - 1 LIMIT 1`,
		`SELECT 1 FROM broker_revisions AS r WHERE binding_count <> (SELECT count(*) FROM broker_bindings AS b
		 WHERE b.registration_id = r.registration_id AND b.revision = r.revision) LIMIT 1`,
	}
	for _, query := range checks {
		rows, err := tx.Query(query)
		if err != nil {
			return ErrUnavailable
		}
		found := rows.Next()
		if err := rows.Close(); found || err != nil || rows.Err() != nil {
			return ErrUnavailable
		}
	}
	var n int
	if tx.QueryRow("SELECT count(*) FROM broker_registrations").Scan(&n) != nil || n > MaxRegistrations {
		return ErrUnavailable
	}
	if err := verifyDeploymentState(tx); err != nil {
		return err
	}
	return checkCapacity(tx, 0, 0)
}

func checkCapacity(tx *sql.Tx, newRevisions, newBindings int) error {
	var revisions, bindings int64
	if tx.QueryRow("SELECT count(*), coalesce(sum(binding_count), 0) FROM broker_revisions").Scan(&revisions, &bindings) != nil ||
		revisions < 0 || bindings < 0 || revisions > MaxTotalRevisions || bindings > MaxTotalBindingRows {
		return ErrUnavailable
	}
	if revisions+int64(newRevisions) > MaxTotalRevisions || bindings+int64(newBindings) > MaxTotalBindingRows {
		return ErrCapacity
	}
	return nil
}
