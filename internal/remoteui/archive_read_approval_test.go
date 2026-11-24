package remoteui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/targetregistry"
)

func TestArchiveReadApprovalExactIdentityExpiryAndRevocation(t *testing.T) {
	ctx := context.Background()
	s := newArchiveReadApprovals()
	now := time.Unix(100, 0)
	s.now = func() time.Time { return now }
	q := archiveReadSelection{actor: archiveReadActor{operator: "admin", session: deploymentSession("generated-cookie")}, locator: snapshothistory.ObjectID{1}, enrollment: targetregistry.HistoryIncarnation{2}, target: strings.Repeat("a", 64), stack: "synthetic", scope: "02" + strings.Repeat("0", 30)}
	issue := func() string {
		t.Helper()
		id, err := s.issue(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	id := issue()
	for _, tc := range []struct {
		name   string
		change func(*archiveReadSelection)
	}{
		{"operator", func(v *archiveReadSelection) { v.actor.operator = "other" }},
		{"session", func(v *archiveReadSelection) { v.actor.session = deploymentSession("other-cookie") }},
		{"locator", func(v *archiveReadSelection) { v.locator[0]++ }},
		{"enrollment", func(v *archiveReadSelection) { v.enrollment[0]++ }},
		{"target", func(v *archiveReadSelection) { v.target = strings.Repeat("b", 64) }},
		{"stack", func(v *archiveReadSelection) { v.stack = "other" }},
		{"scope", func(v *archiveReadSelection) { v.scope = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := q
			tc.change(&bad)
			if s.use(ctx, id, bad, false) || s.use(ctx, id, bad, true) {
				t.Fatal("foreign approval accepted")
			}
		})
	}
	s.revoke(id, archiveReadActor{operator: "other", session: q.actor.session})
	if !s.use(ctx, id, q, false) || !s.use(ctx, id, q, true) || s.use(ctx, id, q, false) {
		t.Fatal("single-use semantics changed")
	}
	id = issue()
	now = now.Add(archiveReadApprovalTTL)
	if s.use(ctx, id, q, true) {
		t.Fatal("expired approval accepted")
	}
	id = issue()
	s.revoke(id, q.actor)
	if s.use(ctx, id, q, true) {
		t.Fatal("revoked approval accepted")
	}
	id = issue()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if s.use(cancelled, id, q, false) || newArchiveReadApprovals().use(ctx, id, q, false) {
		t.Fatal("cancelled or restarted approval accepted")
	}
	for i := 1; i < maxArchiveReadApprovals; i++ {
		issue()
	}
	if _, err := s.issue(ctx, q); err != errRestoreReview {
		t.Fatalf("approval table did not fail closed at capacity: %v", err)
	}
}

func TestArchiveReadIssueRequiresExactAuthenticatedCookie(t *testing.T) {
	a, err := auth.New(auth.Config{User: "admin", Password: "generated-password"})
	if err != nil {
		t.Fatal(err)
	}
	h := &Deployment{auth: a, history: &attendedHistory{archiveRead: newArchiveReadApprovals()}}
	login := func() *http.Cookie {
		t.Helper()
		r := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=generated-password"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		a.HandleLogin(w, r)
		cookies := w.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatal("login did not create session")
		}
		return cookies[0]
	}
	var reviewed archiveReadSelection
	var issued string
	var issueErr error
	sink := a.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if reviewed.actor == (archiveReadActor{}) {
			actor, ok := h.archiveReadActor(r)
			if !ok {
				t.Fatal("missing authenticated actor")
			}
			reviewed = archiveReadSelection{actor: actor, locator: snapshothistory.ObjectID{1}, enrollment: targetregistry.HistoryIncarnation{2}, target: strings.Repeat("a", 64), stack: "fixture", scope: "02" + strings.Repeat("0", 30)}
		}
		issued, issueErr = h.approveArchiveRead(r, reviewed)
	}))
	csrfEndpoint := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(a.CSRFFor(r))) }))
	call := func(cookie *http.Cookie) {
		t.Helper()
		get := httptest.NewRequest("GET", "/fixture", nil)
		get.AddCookie(cookie)
		w := httptest.NewRecorder()
		csrfEndpoint.ServeHTTP(w, get)
		r := httptest.NewRequest("POST", "/fixture", nil)
		r.AddCookie(cookie)
		r.Header.Set("X-CSRF-Token", w.Body.String())
		sink.ServeHTTP(httptest.NewRecorder(), r)
	}
	call(login())
	if issued == "" || issueErr != nil {
		t.Fatalf("exact session denied: %v", issueErr)
	}
	call(login())
	if issued != "" || issueErr != errRestoreReview {
		t.Fatal("same operator's other session reused reviewed consent")
	}
	request := httptest.NewRequest("GET", "/fixture", nil)
	if _, err := h.approveArchiveRead(request, reviewed); err != errRestoreReview {
		t.Fatal("unauthenticated caller issued approval")
	}
}
