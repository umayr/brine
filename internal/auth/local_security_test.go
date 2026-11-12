package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNoAuthBrowserBoundary(t *testing.T) {
	a, err := New(Config{NoAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	handler := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, tc := range []struct {
		host, origin, fetch string
		want                int
	}{
		{"127.0.0.1:8080", "", "", 204},
		{"localhost:8080", "http://localhost:8080", "same-origin", 204},
		{"[::1]:8080", "http://[::1]:8080", "same-origin", 204},
		{"attacker.example:8080", "", "", 403},
		{"127.0.0.1:8080", "https://attacker.example", "", 403},
		{"127.0.0.1:8080", "null", "", 403},
		{"127.0.0.1:8080", "", "cross-site", 403},
	} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/stacks", nil)
		r.Host = tc.host
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		if tc.fetch != "" {
			r.Header.Set("Sec-Fetch-Site", tc.fetch)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("host=%q origin=%q fetch=%q: got %d want %d", tc.host, tc.origin, tc.fetch, w.Code, tc.want)
		}
	}
}
