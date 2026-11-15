//go:build linux

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/operationhistory"
)

func TestAttendedRealHistoryTerminalWriteFaultSuppressesMarker(t *testing.T) {
	for _, phase := range []string{"preview", "apply"} {
		t.Run(phase, func(t *testing.T) {
			home, err := os.UserHomeDir()
			if err != nil {
				t.Fatal(err)
			}
			home, err = filepath.EvalSymlinks(home)
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.MkdirTemp(home, ".brine-attended-fault-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(root)
			data, history, coord := filepath.Join(root, "data"), filepath.Join(root, "history"), filepath.Join(root, "coord")
			for _, path := range []string{data, history, coord} {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			s, err := operationhistory.Open(history, data, coord)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			r, _, capture, h := fakeFlow(t)
			h.history = s
			h.attendedHistory = durableAttendedHistory{store: s}
			failWrites := func() {
				if err := os.Chmod(history, 0500); err != nil {
					t.Fatal(err)
				}
			}
			defer os.Chmod(history, 0700)
			if phase == "preview" {
				capture.previewHook = failWrites
				w := fakeRequest(r, "/api/stacks/synthetic/operations/preview", `{"action":"up"}`)
				if w.Code != 503 || strings.Contains(w.Body.String(), "confirmationToken") || capture.applies.Load() != 0 {
					t.Fatalf("faulted preview: %d %s", w.Code, w.Body.String())
				}
				return
			}
			body := fakeReview(t, r)
			capture.applyHook = failWrites
			w := fakeRequest(r, "/api/stacks/synthetic/up", body)
			if strings.Contains(w.Body.String(), "FORBIDDEN_MARKER") || strings.Contains(w.Body.String(), "FAKE_MARKER") {
				t.Fatal("marker escaped failed durable terminal")
			}
			if again := fakeRequest(r, "/api/stacks/synthetic/up", body); again.Code != 409 {
				t.Fatalf("failed apply token reused: %d", again.Code)
			}
		})
	}
}

type disconnectedWriter struct {
	header http.Header
	status int
}

func (w *disconnectedWriter) Header() http.Header       { return w.header }
func (w *disconnectedWriter) WriteHeader(code int)      { w.status = code }
func (w *disconnectedWriter) Write([]byte) (int, error) { return 0, errors.New("synthetic disconnect") }
func (w *disconnectedWriter) Flush()                    {}

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
	defer func() { _ = s.Close() }()
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
	disconnectBody := fakeReview(t, r)
	disconnectReq := httptest.NewRequest("POST", "/api/stacks/synthetic/up", strings.NewReader(disconnectBody))
	disconnectReq.Header.Set("Authorization", "Bearer token")
	disconnect := &disconnectedWriter{header: make(http.Header)}
	r.ServeHTTP(disconnect, disconnectReq)
	disconnectedID := disconnect.header.Get("X-Brine-Request-ID")
	disconnected, err := s.GetV2(disconnectedID)
	if err != nil || disconnected.Phase != "terminal" || disconnected.Delivery != "write_failed" || disconnected.Apply == nil || disconnected.Apply.Status != "unknown" {
		t.Fatalf("delivery axes: %+v %v", disconnected, err)
	}
	if again := fakeRequest(r, "/api/stacks/synthetic/up", disconnectBody); again.Code != 409 {
		t.Fatal("disconnected token reused", again.Code)
	}
	pending := fakeReview(t, r)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = operationhistory.Open(history, data, coord)
	if err != nil {
		t.Fatal(err)
	}
	h.history = s
	h.attendedHistory = durableAttendedHistory{store: s}
	h.attended = &AttendedContext{producer: fakeProducer{cap}}
	if after := fakeRequest(r, "/api/stacks/synthetic/up", pending); after.Code != 409 {
		t.Fatal("reopened store revived confirmation", after.Code)
	}
	if cap.applies.Load() != 2 {
		t.Fatal("restart launched apply")
	}
	unauth := httptest.NewRecorder()
	r.ServeHTTP(unauth, httptest.NewRequest("GET", "/api/operations/"+id, nil))
	if unauth.Code != 401 {
		t.Fatalf("unauthenticated lookup %d", unauth.Code)
	}
	if err := os.WriteFile(filepath.Join(history, id+".json"), []byte(`{"private":"SENTINEL"}`), 0600); err != nil {
		t.Fatal(err)
	}
	corruptReq := httptest.NewRequest("GET", "/api/operations/"+id, nil)
	corruptReq.Header.Set("Authorization", "Bearer token")
	corrupt := httptest.NewRecorder()
	r.ServeHTTP(corrupt, corruptReq)
	if corrupt.Code != 503 || strings.Contains(corrupt.Body.String(), "SENTINEL") {
		t.Fatalf("corruption was not safely denied: %d %s", corrupt.Code, corrupt.Body.String())
	}
}
