package remoteui

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLegacyEnrollmentRoutesDisabled(t *testing.T) {
	_, f, h, cookie, csrf := setup(t)
	get := httptest.NewRequest("GET", "/remote/review?host=h", nil)
	get.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, get)
	if w.Code == 200 || strings.Contains(w.Body.String(), "reviewToken") {
		t.Fatal("legacy review reachable")
	}
	v := url.Values{"_csrf": {csrf}, "confirm": {"yes"}, "reviewToken": {"old"}}
	post := httptest.NewRequest("POST", "/remote/enroll", strings.NewReader(v.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, post)
	if w.Code == 200 || f.enrolls != 0 {
		t.Fatal("legacy enrollment reachable")
	}
}
