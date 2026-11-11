package web

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/store"
)

type inventoryStore struct {
	store.StackStore
	stacks []*store.Stack
	err    error
}

func (s inventoryStore) List() ([]*store.Stack, error) { return s.stacks, s.err }

func TestStackInventoryStates(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store inventoryStore
		want  []string
	}{
		{"empty", inventoryStore{}, []string{"No stacks yet", "0 TOTAL", "New stack"}},
		{"error", inventoryStore{err: errors.New("store <offline>")}, []string{"Inventory is unavailable", "store &lt;offline&gt;", "Retry", "hx-sync=\"#stack-inventory:replace\""}},
		{"populated", inventoryStore{stacks: []*store.Stack{{Name: "example"}}}, []string{"example", "1 TOTAL", "Deploy", "Edit", "Delete"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{store: tc.store}
			w := httptest.NewRecorder()
			h.stackInventory(w, httptest.NewRequest("GET", "/web/stacks", nil))
			if w.Code != 200 {
				t.Fatalf("status %d", w.Code)
			}
			for _, want := range tc.want {
				if !strings.Contains(w.Body.String(), want) {
					t.Errorf("missing %q in %s", want, w.Body.String())
				}
			}
		})
	}
}
