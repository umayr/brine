//go:build linux

package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/operationhistory"
)

// Linux GHA only: exercises the actual store with synthetic invocations; it is
// not native application/source/provider qualification or a runtime test.
func TestAttendedRealHistoryPreviewApplyAndLookup(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(home, ".brine-attended-history-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	data, history, coord := filepath.Join(root, "data"), filepath.Join(root, "history"), filepath.Join(root, "coord")
	for _, p := range []string{data, history, coord} {
		if err = os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	s, err := operationhistory.Open(history, data, coord)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, _, cap, h := fakeFlow(t)
	h.history = s
	h.attendedHistory = durableAttendedHistory{store: s}
	r.Get("/api/operations/{id}", h.operationGet)
	body := fakeReview(t, r)
	w := fakeRequest(r, "/api/stacks/synthetic/up", body)
	if w.Code != 200 || cap.applies.Load() != 1 {
		t.Fatalf("apply %d %s", w.Code, w.Body.String())
	}
	id := w.Header().Get("X-Brine-Request-ID")
	req := httptest.NewRequest("GET", "/api/operations/"+id, nil)
	req.Header.Set("Authorization", "Bearer token")
	got := httptest.NewRecorder()
	r.ServeHTTP(got, req)
	var record operationhistory.RecordV2
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &record) != nil || record.Kind != "apply" || record.Phase != "terminal" || record.Apply == nil || record.Apply.Status != "unknown" {
		t.Fatalf("lookup %d %s", got.Code, got.Body.String())
	}
	if strings.Contains(got.Body.String(), "confirmationToken") {
		t.Fatal("history exposed confirmation")
	}
	if again := fakeRequest(r, "/api/stacks/synthetic/up", body); again.Code != 409 {
		t.Fatal("replay accepted", again.Code)
	}
}
