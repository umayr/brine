package secretbroker

import (
	"database/sql"
)

func (s *Store) ScopesForStack(stack string) ([]Scope, error) {
	if !token(stack, maxNameLength, "_.-") {
		return nil, ErrInvalidInput
	}
	out := []Scope{}
	err := s.withTx(func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT id,stack_name,target_id FROM broker_registrations WHERE stack_name=? ORDER BY id", stack)
		if err != nil {
			return ErrUnavailable
		}
		defer rows.Close()
		for rows.Next() {
			var scope Scope
			if rows.Scan(&scope.RegistrationID, &scope.StackName, &scope.TargetRegistration) != nil || !validScope(scope) || len(out) >= MaxRegistrations {
				return ErrUnavailable
			}
			out = append(out, scope)
		}
		if rows.Err() != nil {
			return ErrUnavailable
		}
		return nil
	})
	return out, err
}
