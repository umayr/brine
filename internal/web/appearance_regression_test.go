package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
)

func TestAuthenticatedAppearanceAndPublicAssets(t *testing.T) {
	authn, err := auth.New(auth.Config{User: "admin", Password: "test-only-password"})
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(authn.Middleware)
	Mount(router, &Handler{auth: authn, engine: "podman"}, httpx.Timeouts{})

	// Login styling must work before a session exists. Exercise the embedded
	// files through the real static route and auth middleware, not disk reads.
	for _, path := range []string{
		"/static/theme.js", "/static/theme.css", "/static/ui.js",
		"/static/vendor/app.css", "/static/vendor/editor.js",
		"/static/fonts/IBMPlexMono-Regular.ttf", "/static/fonts/IBMPlexMono-Medium.ttf",
		"/static/fonts/IBMPlexMono-SemiBold.ttf", "/static/fonts/OFL.txt",
		"/static/icons/circle-check.svg", "/static/icons/circle-x.svg",
		"/static/icons/circle-help.svg", "/static/icons/circle-minus.svg",
		"/static/icons/clock-3.svg", "/static/icons/info.svg",
		"/static/icons/loader-circle.svg", "/static/icons/triangle-alert.svg", "/static/icons/LICENSE",
	} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK || response.Body.Len() == 0 {
				t.Fatalf("public asset: status %d, bytes %d", response.Code, response.Body.Len())
			}
		})
	}

	form := url.Values{"user": {"admin"}, "password": {"test-only-password"}, "next": {"/settings"}}
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	login := httptest.NewRecorder()
	authn.HandleLogin(login, request)
	if login.Code != http.StatusSeeOther || login.Header().Get("Location") != "/settings" {
		t.Fatalf("login: %d, location %q", login.Code, login.Header().Get("Location"))
	}
	request = httptest.NewRequest(http.MethodGet, "/settings", nil)
	for _, cookie := range login.Result().Cookies() {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, "template error") {
		t.Fatalf("authenticated settings: %d, body %s", response.Code, body)
	}
	for _, want := range []string{`action="/logout"`, `name="_csrf" value="`, "admin", `aria-current="page"`, "podman"} {
		if !strings.Contains(body, want) {
			t.Errorf("authenticated settings missing %q", want)
		}
	}
	if strings.Contains(body, `name="_csrf" value=""`) || strings.Contains(body, "test-only-password") {
		t.Error("settings must include CSRF protection, not credentials")
	}
}

// Embed interfaces so unexpected calls fail rather than silently emulating
// unrelated engine/store behavior. These doubles perform no infrastructure IO.
type dashboardStore struct {
	store.StackStore
	stacks []*store.Stack
	err    error
}

func (s dashboardStore) List() ([]*store.Stack, error) { return s.stacks, s.err }

type dashboardEngine struct {
	engine.Engine
	containers   []engine.ContainerInfo
	images       []engine.ImageInfo
	containerErr error
	imageErr     error
}

func (e dashboardEngine) ListContainers(context.Context) ([]engine.ContainerInfo, error) {
	return e.containers, e.containerErr
}
func (e dashboardEngine) ListImages(context.Context) ([]engine.ImageInfo, error) {
	return e.images, e.imageErr
}

func TestDashboardAvailability(t *testing.T) {
	unavailable := errors.New("private backend connection detail")
	for _, tc := range []struct {
		name            string
		stackErr        error
		containerErr    error
		imageErr        error
		populated       bool
		want, notWanted []string
	}{
		{
			name:      "empty is not unavailable",
			want:      []string{"No stacks yet.", "No running containers.", ">0</strong>"},
			notWanted: []string{"Stacks unavailable", "Containers unavailable", "engine unavailable"},
		},
		{
			name: "stopped containers do not produce an empty running table", populated: true,
			want:      []string{"No running containers.", "example-stack", " / 1</span>"},
			notWanted: []string{"stopped-container", "Containers unavailable", "Stacks unavailable"},
		},
		{
			name: "stack failure is independent", stackErr: unavailable,
			want:      []string{"Stacks unavailable", "No running containers."},
			notWanted: []string{"No stacks yet.", "Containers unavailable", "engine unavailable"},
		},
		{
			name: "container failure is independent", containerErr: unavailable,
			want:      []string{"Containers unavailable", "No stacks yet."},
			notWanted: []string{"No running containers.", "Stacks unavailable"},
		},
		{
			name: "image failure is independent", imageErr: unavailable,
			want:      []string{"engine unavailable", "No running containers.", "No stacks yet."},
			notWanted: []string{"Containers unavailable", "Stacks unavailable"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := dashboardStore{err: tc.stackErr}
			eng := dashboardEngine{containerErr: tc.containerErr, imageErr: tc.imageErr}
			if tc.populated {
				st.stacks = []*store.Stack{{Name: "example-stack"}}
				eng.containers = []engine.ContainerInfo{{Name: "stopped-container", State: "exited"}}
			}
			h := &Handler{store: st, agent: agent.NewLocalAgent(eng, nil)}
			response := httptest.NewRecorder()
			h.dashboard(response, httptest.NewRequest(http.MethodGet, "/", nil))
			body := response.Body.String()
			if response.Code != http.StatusOK || strings.Contains(body, "template error") {
				t.Fatalf("dashboard: %d, body %s", response.Code, body)
			}
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q", want)
				}
			}
			for _, unwanted := range append(tc.notWanted, unavailable.Error()) {
				if strings.Contains(body, unwanted) {
					t.Errorf("unexpected %q", unwanted)
				}
			}
		})
	}
}

func TestRecipeRowsEscapeMetadata(t *testing.T) {
	rs, err := store.NewFSRecipeStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rs.Create(`A "quoted" <img src=x onerror=alert(1)>`, `<script>alert(2)</script>`, `<svg onload=alert(3)>`, "// body"); err != nil {
		t.Fatal(err)
	}
	h := &Handler{recipes: rs}
	response := httptest.NewRecorder()
	h.recipeRows(response, httptest.NewRequest(http.MethodGet, "/web/recipes", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "ui-button") {
		t.Fatalf("recipe rows: %d, body %s", response.Code, body)
	}
	for _, raw := range []string{"<img", "<script", "<svg", `hx-confirm="Delete recipe "`} {
		if strings.Contains(body, raw) {
			t.Errorf("unsafe recipe fragment contains %q", raw)
		}
	}
	for _, escaped := range []string{"&lt;img", "&lt;script", "&lt;svg", "&#34;quoted&#34;"} {
		if !strings.Contains(body, escaped) {
			t.Errorf("recipe rows missing escaped value %q", escaped)
		}
	}
}
