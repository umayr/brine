package secretbroker

import (
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// Store is a handle to explicitly provisioned binding state. It is safe for
// concurrent callers. SQLite serializes transactions across separate handles.
type Store struct{ state *storeState }

type storeState struct {
	mu       sync.Mutex
	db       *sql.DB
	path     string
	files    stateFiles
	identity string
}

func (Store) Format(w fmt.State, _ rune) { redact(w, "Store") }

// Identity returns the durable identity recorded at Open. It is unchanged by
// mutations and can be pinned by callers to detect a different provisioned DB.
func (s *Store) Identity() string {
	if s == nil || s.state == nil {
		return ""
	}
	return s.state.identity
}

func (s *Store) Close() error {
	if s == nil || s.state == nil {
		return nil
	}
	st := s.state
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.db == nil {
		return nil
	}
	err := st.db.Close()
	st.db = nil
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *Store) withTx(apply func(*sql.Tx) error) error {
	if s == nil || s.state == nil {
		return ErrUnavailable
	}
	st := s.state
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.db == nil {
		return ErrUnavailable
	}
	if err := st.files.check(st.path, true); err != nil {
		return err
	}
	tx, err := st.db.Begin() // BEGIN IMMEDIATE, including scope and CAS checks.
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	id, err := readIdentity(tx)
	if err != nil || id != st.identity {
		return ErrUnavailable
	}
	if err := apply(tx); err != nil {
		return err
	}
	if err := st.files.check(st.path, true); err != nil {
		return err
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return st.files.check(st.path, true)
}

// Register creates the immutable scope and a revision-zero audit event.
// Duplicate registration is denied, including attempts to change the scope.
func (s *Store) Register(scope Scope, actor string) error {
	if !validScope(scope) || !validActor(actor) {
		return ErrInvalidInput
	}
	return s.withTx(func(tx *sql.Tx) error {
		switch err := checkScope(tx, scope); err {
		case nil:
			return ErrRegistrationExists
		case ErrUnregistered:
			// Explicit registration is the only operation that can create a scope.
		default:
			return err
		}
		var n int
		if tx.QueryRow("SELECT count(*) FROM broker_registrations").Scan(&n) != nil || n > MaxRegistrations {
			return ErrUnavailable
		}
		if n == MaxRegistrations {
			return ErrCapacity
		}
		if err := checkCapacity(tx, 1, 0); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO broker_registrations (id, stack_name, target_id) VALUES (?, ?, ?)`,
			scope.RegistrationID, scope.StackName, scope.TargetRegistration); err != nil {
			return ErrUnavailable
		}
		return insertRevision(tx, scope, 0, actor, 0)
	})
}

// Save atomically appends a snapshot and its audit event iff expectedRevision
// matches the current revision. An empty snapshot is an unbind, not a reset.
func (s *Store) Save(scope Scope, expectedRevision int64, actor string, bindings []Binding) (int64, error) {
	if !validScope(scope) || expectedRevision < 0 || !validActor(actor) {
		return 0, ErrInvalidInput
	}
	if len(bindings) > MaxBindingsPerRevision {
		return 0, ErrCapacity
	}
	seen := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		if !validBinding(binding) || seen[binding.Name] {
			return 0, ErrInvalidInput
		}
		seen[binding.Name] = true
	}
	var revision int64
	err := s.withTx(func(tx *sql.Tx) error {
		if err := checkScope(tx, scope); err != nil {
			return err
		}
		head, _, err := headRevision(tx, scope)
		if err != nil {
			return err
		}
		if head != expectedRevision {
			return ErrConflict
		}
		revision = head + 1
		if revision >= MaxRevisionsPerScope {
			return ErrCapacity
		}
		if err := checkCapacity(tx, 1, len(bindings)); err != nil {
			return err
		}
		if err := insertRevision(tx, scope, revision, actor, len(bindings)); err != nil {
			return err
		}
		return insertBindings(tx, scope, revision, bindings)
	})
	if err != nil {
		return 0, err
	}
	return revision, nil
}

// Load reads the head snapshot. A registered scope with zero bindings is not
// interchangeable with an unregistered scope or unavailable store.
func (s *Store) Load(scope Scope) (Snapshot, error) { return s.load(scope, nil) }

// LoadRevision reads a detached copy of an immutable historical snapshot.
func (s *Store) LoadRevision(scope Scope, revision int64) (Snapshot, error) {
	if revision < 0 {
		return Snapshot{}, ErrInvalidInput
	}
	return s.load(scope, &revision)
}

func (s *Store) load(scope Scope, requested *int64) (Snapshot, error) {
	if !validScope(scope) {
		return Snapshot{}, ErrInvalidInput
	}
	var snapshot Snapshot
	err := s.withTx(func(tx *sql.Tx) error {
		if err := checkScope(tx, scope); err != nil {
			return err
		}
		rev, count, err := headRevision(tx, scope)
		if err != nil {
			return err
		}
		if requested != nil && *requested != rev {
			if *requested > rev {
				return ErrRevisionNotFound
			}
			rev = *requested
			if tx.QueryRow(`SELECT binding_count FROM broker_revisions WHERE registration_id = ? AND revision = ?`,
				scope.RegistrationID, rev).Scan(&count) != nil {
				return ErrUnavailable
			}
		}
		bindings, err := loadBindings(tx, scope, rev, count)
		if err != nil {
			return err
		}
		snapshot = Snapshot{Scope: scope, Revision: rev, Bindings: bindings}
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

// History returns metadata-only audit events in increasing revision order.
func (s *Store) History(scope Scope) ([]AuditEvent, error) {
	if !validScope(scope) {
		return nil, ErrInvalidInput
	}
	var history []AuditEvent
	err := s.withTx(func(tx *sql.Tx) error {
		if err := checkScope(tx, scope); err != nil {
			return err
		}
		rows, err := tx.Query(`SELECT revision, actor, at_ns, binding_count FROM broker_revisions
			WHERE registration_id = ? ORDER BY revision`, scope.RegistrationID)
		if err != nil {
			return ErrUnavailable
		}
		defer rows.Close()
		for rows.Next() {
			var event AuditEvent
			var at int64
			if rows.Scan(&event.Revision, &event.Actor, &at, &event.BindingCount) != nil || !validActor(event.Actor) ||
				at <= 0 || len(history) >= MaxRevisionsPerScope || event.Revision != int64(len(history)) ||
				!validRevisionMetadata(event.Revision, event.BindingCount) {
				return ErrUnavailable
			}
			event.At = time.Unix(0, at).UTC()
			switch {
			case event.Revision == 0:
				event.Kind = EventRegister
			case event.BindingCount == 0:
				event.Kind = EventUnbind
			default:
				event.Kind = EventSave
			}
			history = append(history, event)
		}
		if rows.Err() != nil || len(history) == 0 {
			return ErrUnavailable
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return history, nil
}

func checkScope(tx *sql.Tx, scope Scope) error {
	stored := Scope{RegistrationID: scope.RegistrationID}
	err := tx.QueryRow(`SELECT stack_name, target_id FROM broker_registrations WHERE id = ?`, scope.RegistrationID).
		Scan(&stored.StackName, &stored.TargetRegistration)
	if err == sql.ErrNoRows {
		return ErrUnregistered
	}
	if err != nil || !validScope(stored) {
		return ErrUnavailable
	}
	if stored != scope {
		return ErrScopeMismatch
	}
	return nil
}

func validRevisionMetadata(revision int64, count int) bool {
	return revision >= 0 && revision < MaxRevisionsPerScope && count >= 0 && count <= MaxBindingsPerRevision &&
		(revision != 0 || count == 0)
}

func headRevision(tx *sql.Tx, scope Scope) (int64, int, error) {
	var revision int64
	var count int
	if tx.QueryRow(`SELECT revision, binding_count FROM broker_revisions WHERE registration_id = ?
		ORDER BY revision DESC LIMIT 1`, scope.RegistrationID).Scan(&revision, &count) != nil || !validRevisionMetadata(revision, count) {
		return 0, 0, ErrUnavailable
	}
	return revision, count, nil
}

func insertRevision(tx *sql.Tx, scope Scope, revision int64, actor string, count int) error {
	if _, err := tx.Exec(`INSERT INTO broker_revisions (registration_id, revision, actor, at_ns, binding_count)
		VALUES (?, ?, ?, ?, ?)`, scope.RegistrationID, revision, actor, time.Now().UnixNano(), count); err != nil {
		return ErrUnavailable
	}
	return nil
}

func insertBindings(tx *sql.Tx, scope Scope, revision int64, bindings []Binding) error {
	if len(bindings) == 0 {
		return nil
	}
	stmt, err := tx.Prepare(`INSERT INTO broker_bindings (registration_id, revision, name, connection_id, kind,
		vault_path, vault_property, vault_version, infisical_folder, infisical_key, infisical_version, local_alias)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return ErrUnavailable
	}
	defer stmt.Close()
	for _, b := range bindings {
		if _, err := stmt.Exec(scope.RegistrationID, revision, b.Name, b.ConnectionID, string(b.Kind),
			b.Vault.Path, b.Vault.Property, b.Vault.Version, b.Infisical.Folder, b.Infisical.Key, b.Infisical.Version,
			b.LocalAlias); err != nil {
			return ErrUnavailable
		}
	}
	return nil
}

func loadBindings(tx *sql.Tx, scope Scope, revision int64, count int) ([]Binding, error) {
	if !validRevisionMetadata(revision, count) {
		return nil, ErrUnavailable
	}
	rows, err := tx.Query(`SELECT name, connection_id, kind, vault_path, vault_property, vault_version,
		infisical_folder, infisical_key, infisical_version, local_alias FROM broker_bindings
		WHERE registration_id = ? AND revision = ? ORDER BY name`, scope.RegistrationID, revision)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	bindings := make([]Binding, 0, count)
	for rows.Next() {
		var b Binding
		var kind string
		if len(bindings) >= count || rows.Scan(&b.Name, &b.ConnectionID, &kind, &b.Vault.Path, &b.Vault.Property, &b.Vault.Version,
			&b.Infisical.Folder, &b.Infisical.Key, &b.Infisical.Version, &b.LocalAlias) != nil {
			return nil, ErrUnavailable
		}
		b.Kind = ProviderKind(kind)
		if !validBinding(b) {
			return nil, ErrUnavailable
		}
		bindings = append(bindings, b)
	}
	if rows.Err() != nil || len(bindings) != count {
		return nil, ErrUnavailable
	}
	return bindings, nil
}
