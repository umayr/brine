package web

import (
	"errors"
	"net/http/httptest"
	"regexp"
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

func TestStackInventoryControlIDsAreUnique(t *testing.T) {
	h := &Handler{store: inventoryStore{stacks: []*store.Stack{{Name: "foo"}, {Name: "edit-foo"}, {Name: "new"}, {Name: "a_B-9"}}}}
	w := httptest.NewRecorder()
	h.stackInventory(w, httptest.NewRequest("GET", "/web/stacks", nil))
	ids := regexp.MustCompile(`\bid="([^"]+)"`).FindAllStringSubmatch(w.Body.String(), -1)
	seen := make(map[string]bool)
	for _, match := range ids {
		if seen[match[1]] {
			t.Errorf("duplicate ID %q", match[1])
		}
		seen[match[1]] = true
	}
	for _, name := range []string{"foo", "edit-foo", "new", "a_B-9"} {
		for _, action := range []string{"open", "edit", "deploy", "delete"} {
			id := "stack-inventory-" + action + "-" + name
			if !seen[id] {
				t.Errorf("missing ID %q", id)
			}
		}
	}
}
