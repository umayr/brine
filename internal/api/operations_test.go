package api

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/httpx"
)

func TestOperationLookupDefenseOnNoAuthAndSyntheticMounts(t *testing.T) {
	for _, noAuth := range []bool{false, true} {
		a, e := auth.New(auth.Config{NoAuth: noAuth, User: "synthetic", Password: "synthetic", APIToken: "PRIVATE_TOKEN_SENTINEL"})
		if e != nil {
			t.Fatal(e)
		}
		r := chi.NewRouter()
		r.Use(a.Middleware)
		Mount(r, nil, nil, nil, nil, "docker", httpx.Timeouts{}, time.Second)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1/api/operations/br_"+strings.Repeat("a", 32), nil))
		if w.Code != 401 || strings.Contains(w.Body.String(), "PRIVATE") {
			t.Fatalf("lookup auth defense noAuth=%v: %d %s", noAuth, w.Code, w.Body.String())
		}
		if !noAuth {
			for _, id := range []string{"br_" + strings.Repeat("a", 32), "not-an-id", "op_" + strings.Repeat("a", 32)} {
				req := httptest.NewRequest("GET", "/api/operations/"+id, nil)
				req.Header.Set("Authorization", "Bearer PRIVATE_TOKEN_SENTINEL")
				w = httptest.NewRecorder()
				r.ServeHTTP(w, req)
				if w.Code != 404 || strings.Contains(w.Body.String(), "PRIVATE") {
					t.Fatalf("invalid/missing ID: %d %s", w.Code, w.Body.String())
				}
			}
		}
	}
}
