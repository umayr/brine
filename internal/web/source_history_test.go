package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/httpx"
)

func TestSourceHistoryPostsRequireBrowserSessionAndCSRF(t *testing.T) {
	const password, apiToken = "test-only-password", "test-only-token"
	authn, err := auth.New(auth.Config{User: "admin", Password: password, APIToken: apiToken})
	if err != nil {
		t.Fatal(err)
	}
	noAuth, err := auth.New(auth.Config{NoAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	mount := func(a *auth.Authenticator) *chi.Mux {
		r := chi.NewRouter()
		r.Use(a.Middleware)
		r.Post("/login", a.HandleLogin)
		r.Get("/test/csrf", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Test-CSRF", a.CSRFFor(r))
		})
		// Exercise the authority gate with history present. No case should
		// reach workspace or archive access, so leave both unconfigured.
		Mount(r, &Handler{auth: a, sourceHistory: &sourceHistoryAccess{}}, httpx.Timeouts{})
		return r
	}
	router, noAuthRouter := mount(authn), mount(noAuth)
	post := func(path string, form url.Values) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://localhost"+path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return r
	}

	login := httptest.NewRecorder()
	router.ServeHTTP(login, post("/login", url.Values{"user": {"admin"}, "password": {password}}))
	cookies := login.Result().Cookies()
	if login.Code != http.StatusSeeOther || len(cookies) != 1 || cookies[0].Name != auth.SessionCookie {
		t.Fatalf("login status %d; expected a browser session cookie", login.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/test/csrf", nil)
	request.AddCookie(cookies[0])
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	csrf := response.Header().Get("X-Test-CSRF")
	if response.Code != http.StatusOK || csrf == "" {
		t.Fatalf("authenticated CSRF lookup: status %d, token present %t", response.Code, csrf != "")
	}

	for _, action := range []string{"review", "confirm", "auto-checkpoint", "global-default"} {
		for _, tc := range []struct {
			name, token, wantBody string
			local, bearer, cookie bool
			wantStatus            int
		}{
			{name: "anonymous", wantStatus: http.StatusUnauthorized, wantBody: "authentication required"},
			{name: "local no-auth", local: true, wantStatus: http.StatusForbidden, wantBody: "interactive admin session"},
			{name: "bearer", bearer: true, wantStatus: http.StatusForbidden, wantBody: "interactive admin session"},
			{name: "bearer with cookie and CSRF", bearer: true, cookie: true, token: csrf, wantStatus: http.StatusForbidden, wantBody: "interactive admin session"},
			{name: "missing CSRF", cookie: true, wantStatus: http.StatusForbidden, wantBody: "invalid or missing CSRF"},
			{name: "mismatched CSRF", cookie: true, token: "wrong-" + csrf, wantStatus: http.StatusForbidden, wantBody: "invalid or missing CSRF"},
			{name: "valid cookie and CSRF reach form validation", cookie: true, token: csrf, wantStatus: http.StatusBadRequest, wantBody: "Invalid revision selection"},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				form := url.Values{"kind": {"retain"}}
				if tc.wantStatus == http.StatusBadRequest {
					form.Set("kind", "invalid") // Stop after authentication, before workspace access.
				}
				if tc.token != "" {
					form.Set("_csrf", tc.token)
				}
				path := "/web/stacks/example/history/" + action
				if action == "global-default" {
					path = "/settings/source-history"
				}
				r := post(path, form)
				if tc.cookie {
					r.AddCookie(cookies[0])
				}
				if tc.bearer {
					r.Header.Set("Authorization", "Bearer "+apiToken)
				}
				h := router
				if tc.local {
					h = noAuthRouter
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if (action == "auto-checkpoint" || action == "global-default") && tc.wantStatus == http.StatusBadRequest {
					tc.wantBody = "Invalid checkpoint setting"
				}
				if w.Code != tc.wantStatus || !strings.Contains(w.Body.String(), tc.wantBody) {
					t.Errorf("status %d, body %q; want %d containing %q", w.Code, w.Body.String(), tc.wantStatus, tc.wantBody)
				}
			})
		}
	}
}

func TestSourceHistoryReviewProof(t *testing.T) {
	s := &sourceHistoryAccess{reviews: [32]byte{1}}
	binding := [5]string{"session-one", "example", "restore", strings.Repeat("a", 64), "baseline-one"}
	now := time.Now()
	sign := func(at time.Time) string {
		return s.proof(binding[0], binding[1], binding[2], binding[3], binding[4], at.Unix())
	}
	verify := func(proof string, b [5]string) bool {
		return s.verifyProof(proof, b[0], b[1], b[2], b[3], b[4])
	}
	valid := sign(now.Add(time.Minute))
	if !verify(valid, binding) {
		t.Fatal("fresh proof rejected for the original review")
	}
	for i, field := range []string{"session", "stack name", "operation kind", "revision ID", "baseline"} {
		changed := binding
		changed[i] = "other-" + changed[i]
		if verify(valid, changed) {
			t.Errorf("proof accepted for a different %s", field)
		}
	}
	for name, proof := range map[string]string{
		"missing": "", "malformed": "invalid", "modified": valid + "0",
		"expired": sign(now.Add(-time.Minute)), "too far in future": sign(now.Add(10 * time.Minute)),
	} {
		if verify(proof, binding) {
			t.Errorf("%s proof accepted", name)
		}
	}
}

func TestSourceHistoryComparisonDiff(t *testing.T) {
	const before = "private-before-marker"
	const after = "<script>private-after-marker</script>"
	current := map[string][]byte{"project.pkl": []byte(before)}
	candidate := map[string][]byte{"project.pkl": []byte(after)}
	h := &Handler{}
	for _, compare := range []bool{false, true} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/web/stacks/example/history/review", nil)
		h.renderPartial(w, r, "stack-history", historyPage{
			Stack: "example", Review: "restore", Confirm: true, Compare: compare,
			Files: diffSource(current, candidate, compare),
		})
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "project.pkl") {
			t.Fatalf("compare=%t: status %d, missing comparison metadata", compare, w.Code)
		}
		if compare {
			if !strings.Contains(body, before) || !strings.Contains(body, "&lt;script&gt;private-after-marker&lt;/script&gt;") || strings.Contains(body, after) {
				t.Error("opted-in comparison must render the line-by-line diff with HTML escaping")
			}
		} else if strings.Contains(body, before) || strings.Contains(body, "private-after-marker") {
			t.Error("comparison exposed source without opt-in")
		}
	}
}
