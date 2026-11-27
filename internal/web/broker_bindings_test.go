package web

import (
	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/httpx"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrokerSettingsUnavailableDoesNotClaimEmptyOrHealthy(t *testing.T) {
	h := New(nil, nil, nil, nil, "docker", false, nil, 0)
	r := chi.NewRouter()
	Mount(r, h, httpx.Timeouts{})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/settings/secrets", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Secrets are unavailable") || strings.Contains(w.Body.String(), "Configured · not tested") {
		t.Fatal("unavailable became configured/empty")
	}
	for _, private := range []string{"TokenFile", "ClientSecretFile", "value=\""} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatal("private configuration surfaced")
		}
	}
}
func TestBrokerReferenceMutationRequiresCookieAndConfiguration(t *testing.T) {
	h := New(nil, nil, nil, nil, "docker", false, nil, 0)
	r := chi.NewRouter()
	Mount(r, h, httpx.Timeouts{})
	w := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/stacks/example/secrets", strings.NewReader("value=not-allowed"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(w, request)
	if w.Code != 403 {
		t.Fatal("unauthorized mutation")
	}
}
