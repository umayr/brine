package remoteui

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/targetregistry"
)

func TestProtectedRoutesBoundsBeforeAuthAndRetiresLegacy(t *testing.T) {
	a, f, _, cookie, _ := setup(t)
	reads, err := New(f, a)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "targets"))
	if err != nil {
		t.Fatal(err)
	}
	observer := &enrollmentObserverFixture{}
	enroll, err := NewEnrollmentHandler(a, reg, observer)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := ProtectedRoutes(reads, enroll)
	if err != nil {
		t.Fatal(err)
	}
	body := &countReader{r: strings.NewReader("_csrf=" + strings.Repeat("a", 1<<20))}
	req := httptest.NewRequest("POST", "/remote/enrollment/review", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	routes.ServeHTTP(&enrollmentTestWriter{w}, req)
	if body.n > 32<<10+1 || observer.calls != 0 || w.Code == 200 {
		t.Fatalf("unbounded request: n=%d status=%d", body.n, w.Code)
	}
	req = httptest.NewRequest("GET", "/remote/review?host=h", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	routes.ServeHTTP(&enrollmentTestWriter{w}, req)
	if w.Code == 200 {
		t.Fatal("legacy route active")
	}
	req = httptest.NewRequest("GET", "/remote/inventory?host=host", nil)
	req.Header.Set("Authorization", "Bearer token")
	w = httptest.NewRecorder()
	routes.ServeHTTP(&enrollmentTestWriter{w}, req)
	if w.Code == 200 {
		t.Fatal("bearer remote read accepted")
	}
	req = httptest.NewRequest("POST", "/remote/reveal", strings.NewReader(""))
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	routes.ServeHTTP(&enrollmentTestWriter{w}, req)
	if w.Code != 404 || f.reveals != 0 || len(f.audits) != 0 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("reveal route was not disabled")
	}
}
