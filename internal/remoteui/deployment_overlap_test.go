package remoteui

import "github.com/umayr/brine/internal/store"

// Preserve all normal store behavior except the injected metadata lookup failure.
type overlapErrorStore struct {
	store.StackStore
	err   error
	stack *store.Stack
}

func (s overlapErrorStore) Get(string) (*store.Stack, error) { return s.stack, s.err }
