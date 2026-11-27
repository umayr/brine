package secretbroker

import (
	"database/sql"
)

// StackProtected is registration-based, not a test for nonempty bindings.
func (s *Store) StackProtected(name string) (bool, error) {
	if !token(name, maxNameLength, "_.-") {
		return false, ErrInvalidInput
	}
	var count int
	err := s.withTx(func(tx *sql.Tx) error {
		if tx.QueryRow("SELECT count(*) FROM broker_registrations WHERE stack_name = ?", name).Scan(&count) != nil {
			return ErrUnavailable
		}
		return nil
	})
	return count > 0, err
}
