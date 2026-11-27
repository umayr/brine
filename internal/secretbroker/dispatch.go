package secretbroker

import (
	"context"
	"database/sql"
)

// WithRevisionPin excludes every binding writer, including other Store handles,
// until dispatch returns. fn must not reenter this binding store. An error after
// fn ran does not prove the target was untouched; retain independent outcomes.
func (s *Store) WithRevisionPin(scope Scope, revision int64, fn func(Snapshot) error) error {
	if !validScope(scope) || revision < 0 || fn == nil {
		return ErrInvalidInput
	}
	return s.withTx(func(tx *sql.Tx) error {
		if err := checkScope(tx, scope); err != nil {
			return err
		}
		head, count, err := headRevision(tx, scope)
		if err != nil {
			return err
		}
		if head != revision {
			return ErrConflict
		}
		bindings, err := loadBindings(tx, scope, head, count)
		if err != nil {
			return err
		}
		return fn(Snapshot{Scope: scope, Revision: head, Bindings: bindings})
	})
}

// ChangePolicy serializes a caller-owned immutable policy swap with final
// dispatch. All policy writers must use it. Successful changes invalidate bytes
// still awaiting preparation; already distributed reviews fail authorization.
func (m *Manager) ChangePolicy(change func() error) error {
	if m == nil || change == nil {
		return ErrInvalidInput
	}
	m.policy.Lock()
	defer m.policy.Unlock()
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return ErrUnavailable
	}
	if err := change(); err != nil {
		return ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, p := range m.pending {
		erase(p)
		delete(m.pending, id)
	}
	m.bytes = 0
	return nil
}

// WithCurrent checks live authorization and pins binding+policy through one
// final transport dispatch (including a reference-only apply after Take).
// Authorization callbacks must not reenter the same binding store or swap policy.
func (m *Manager) WithCurrent(ctx context.Context, i ReviewIdentity, dispatch func() error) error {
	if m == nil || ctx == nil || !validReview(i) || dispatch == nil {
		return ErrInvalidInput
	}
	m.policy.RLock()
	defer m.policy.RUnlock()
	return m.store.WithRevisionPin(i.Scope, i.BindingRevision, func(s Snapshot) error {
		m.mu.Lock()
		closed := m.closed
		m.mu.Unlock()
		if closed || ctx.Err() != nil {
			return ErrUnavailable
		}
		if m.authorize(ctx, i, s.Bindings) != nil {
			return ErrConflict
		}
		if ctx.Err() != nil {
			return ErrUnavailable
		}
		return dispatch()
	})
}

// Dispatch consumes frozen bytes once inside the same final revision/policy
// fence used by reference-only Apply. It does not retry ambiguous delivery.
func (m *Manager) Dispatch(ctx context.Context, id string, i ReviewIdentity, send func(Preparation, *Delivery) error) error {
	if !validID(id) || send == nil {
		return ErrInvalidInput
	}
	return m.WithCurrent(ctx, i, func() error {
		meta, delivery, err := m.takePending(id, i)
		if err != nil {
			return err
		}
		defer delivery.Close()
		return send(meta, delivery)
	})
}
