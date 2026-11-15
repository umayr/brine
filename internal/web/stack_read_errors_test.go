package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/store"
)

type brokenStackReads struct {
	store.StackStore
	fail string
}

func (s brokenStackReads) Get(string) (*store.Stack, error) {
	return &store.Stack{Name: "demo", Entrypoint: "compose.pkl"}, nil
}
func (s brokenStackReads) Files(string) ([]string, error) {
	if s.fail == "files" {
		return nil, errors.New("private diagnostic")
	}
	return []string{"compose.pkl"}, nil
}
func (s brokenStackReads) ReadFile(_, name string) (string, error) {
	if s.fail == "read" || (s.fail == "entrypoint" && name == "compose.pkl") {
		return "", errors.New("private diagnostic")
	}
	return "content", nil
}

func TestStackPagesFailClosedOnReadErrors(t *testing.T) {
	for _, tc := range []struct{ page, failure, query string }{
		{"detail", "files", ""}, {"detail", "read", ""},
		{"editor", "files", ""}, {"editor", "read", ""}, {"editor", "entrypoint", "?file=other.pkl"},
	} {
		t.Run(tc.page+"/"+tc.failure, func(t *testing.T) {
			h := &Handler{store: brokenStackReads{fail: tc.failure}}
			r := httptest.NewRequest(http.MethodGet, "/"+tc.query, nil)
			route := chi.NewRouteContext()
			route.URLParams.Add("name", "demo")
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
			w := httptest.NewRecorder()
			if tc.page == "detail" {
				h.stackDetail(w, r)
			} else {
				h.stackEditor(w, r)
			}
			if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "private diagnostic") {
				t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
