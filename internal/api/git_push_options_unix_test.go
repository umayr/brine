//go:build unix

package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
)

func TestGitPushOptionsRejectInvalidBodiesWithoutInvokingGit(t *testing.T) {
	router, calls := newGitPushOptionsRouter(t, false)
	for _, tc := range []struct{ name, body string }{
		{"truncated", "{"},
		{"null body", "null"},
		{"array", "[]"},
		{"null message", `{"message":null}`},
		{"wrong type", `{"message":false}`},
		{"invalid UTF-8", "{\"message\":\"\xff\"}"},
		{"unknown option", `{"unknown":"text"}`},
		{"wrong case", `{"Message":"text"}`},
		{"extra option", `{"message":"text","unknown":"value"}`},
		{"duplicate option", `{"message":"first","message":"second"}`},
		{"escaped duplicate", `{"message":"first","\u006dessage":"second"}`},
		{"second value", `{} {}`},
		{"trailing garbage", `{} trailing`},
		{"oversized message", `{"message":"` + strings.Repeat("a", maxGitPushOptionsBytes) + `"}`},
		{"oversized trailing whitespace", "{}" + strings.Repeat(" ", maxGitPushOptionsBytes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := postGitPushOptions(router, tc.body, true)
			if w.Code != http.StatusBadRequest || strings.TrimSpace(w.Body.String()) != `{"error":"invalid request body"}` {
				t.Errorf("response = %d %q, want generic 400", w.Code, w.Body.String())
			}
			assertGitPushNotInvoked(t, calls)
		})
	}
}

func TestGitPushOptionsPreserveAuthorizationAndDefaults(t *testing.T) {
	const defaultMessage = "chore: update stack via brine"
	for _, tc := range []struct {
		name, body, message string
		readOnly, authed    bool
		status              int
	}{
		{"no body", "", defaultMessage, false, true, http.StatusOK},
		{"whitespace only", " \n\t", defaultMessage, false, true, http.StatusOK},
		{"empty object", `{}`, defaultMessage, false, true, http.StatusOK},
		{"empty message", `{"message":""}`, defaultMessage, false, true, http.StatusOK},
		{"explicit message", `{"message":"synthetic subject\n\nsecond paragraph"}`, "synthetic subject\n\nsecond paragraph", false, true, http.StatusOK},
		{"read-only", "{", "", true, true, http.StatusForbidden},
		{"unauthenticated", "{", "", false, false, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router, calls := newGitPushOptionsRouter(t, tc.readOnly)
			w := postGitPushOptions(router, tc.body, tc.authed)
			if w.Code != tc.status {
				t.Errorf("response status = %d, want %d", w.Code, tc.status)
			}
			if tc.status != http.StatusOK {
				assertGitPushNotInvoked(t, calls)
				return
			}
			got, err := os.ReadFile(calls)
			if err != nil || string(got) != "commit:-m:"+tc.message+"\npush\n" {
				t.Errorf("synthetic Git calls = %q, %v", got, err)
			}
		})
	}
}

const gitPushTestToken = "synthetic-api-token"

func newGitPushOptionsRouter(t *testing.T, readOnly bool) (*chi.Mux, string) {
	t.Helper()
	st, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, err := st.RepoDir("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.pkl"), []byte("// synthetic\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateFromGit("demo", &store.GitSource{URL: "synthetic.invalid", ReadOnly: readOnly}, ""); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "git-recorder")
	// The fake records every invocation in its cwd and never executes Git.
	const script = `#!/bin/sh
if [ "$1" = "-c" ] && [ "$2" = "core.hooksPath=/dev/null" ]; then shift 2; fi
case "$1" in
    commit) printf '%s:%s:%s\n' "$1" "$2" "$3" >> calls;;
    *) printf '%s\n' "$1" >> calls;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(auth.Config{User: "test", Password: "synthetic-password", APIToken: gitPushTestToken})
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(a.Middleware)
	Mount(router, nil, st, nil, &git.Runner{Binary: bin}, "docker", httpx.Timeouts{}, 5*time.Second)
	return router, filepath.Join(dir, "calls")
}

func postGitPushOptions(router *chi.Mux, body string, authed bool) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(http.MethodPost, "/api/stacks/demo/git/push", nil)
	} else {
		r = httptest.NewRequest(http.MethodPost, "/api/stacks/demo/git/push", strings.NewReader(body))
	}
	if authed {
		r.Header.Set("Authorization", "Bearer "+gitPushTestToken)
	}
	w := &deadlineRecorder{httptest.NewRecorder()}
	router.ServeHTTP(w, r)
	return w.ResponseRecorder
}

func assertGitPushNotInvoked(t *testing.T, calls string) {
	t.Helper()
	if _, err := os.Stat(calls); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unexpected synthetic Git invocation or stat error: %v", err)
	}
}
