package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/httpx"
)

func TestAppearanceSettingsRoute(t *testing.T) {
	// Nil engine/store dependencies are deliberate: settings must be usable
	// without contacting an engine, reading stacks, or evaluating source.
	h := &Handler{engine: "podman"}
	router := chi.NewRouter()
	Mount(router, h, httpx.Timeouts{})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("settings status: %d, body: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, want := range []string{
		"Appearance — Brine", "Settings / Appearance", "podman",
		`class="nav-item active" aria-label="Settings"`,
		`id="theme-select"`, `id="density-select"`,
		`data-reset-appearance`, `role="status"`,
		"examples, not live observations",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("settings missing %q", want)
		}
	}
	if got := strings.Count(body, `name="brine-accent"`); got != 6 {
		t.Errorf("accent options: got %d, want 6", got)
	}
	if strings.Contains(body, "template error") {
		t.Fatal("settings render contained a template error")
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/settings", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("settings must not introduce a server write endpoint: %d", response.Code)
	}
}

func TestAppearanceSettingsRequiresAuthentication(t *testing.T) {
	authn, err := auth.New(auth.Config{User: "admin", Password: "test-only-password"})
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(authn.Middleware)
	Mount(router, &Handler{auth: authn}, httpx.Timeouts{})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if response.Code != http.StatusFound || !strings.HasPrefix(response.Header().Get("Location"), "/login?") {
		t.Fatalf("unauthenticated settings: status %d, location %q", response.Code, response.Header().Get("Location"))
	}
	if strings.Contains(response.Body.String(), `id="theme-select"`) {
		t.Fatal("settings leaked through the auth guard")
	}
}
