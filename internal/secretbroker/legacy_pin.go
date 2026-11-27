package secretbroker

import "database/sql"

// WithUnprotected excludes registration/binding writers for the whole legacy
// submission. Callback must not reenter any Store method or policy lifecycle.
func (s *Store) WithUnprotected(name string, fn func()) error {
	if !token(name, maxNameLength, "_.-") || fn == nil {
		return ErrInvalidInput
	}
	return s.withTx(func(tx *sql.Tx) error {
		var n int
		if tx.QueryRow("SELECT count(*) FROM broker_registrations WHERE stack_name = ?", name).Scan(&n) != nil {
			return ErrUnavailable
		}
		if n != 0 {
			return ErrUnavailable
		}
		fn()
		return nil
	})
}
