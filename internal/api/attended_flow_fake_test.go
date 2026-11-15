package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/operationhistory"
	"github.com/umayr/brine/internal/store"
)

// These fakes model callback ordering, not durable persistence.
type fakeAttendedHistory struct {
	mu                     sync.Mutex
	preview                operationhistory.RecordV2
	failPreview, failApply bool
	applies                int
}
type fakeAttendedRequest struct {
	history  *fakeAttendedHistory
	kind, id string
}

func (h *fakeAttendedHistory) BeginPreview(i operationhistory.BoundIntent) (attendedRequest, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.preview = operationhistory.RecordV2{Version: operationhistory.VersionV2, RequestID: "br_" + strings.Repeat("a", 32), Kind: "preview", Intent: i, Phase: "accepted", Persistence: "durable"}
	return &fakeAttendedRequest{h, "preview", h.preview.RequestID}, nil
}
func (h *fakeAttendedHistory) BeginApply(i operationhistory.BoundIntent, p, d string) (attendedRequest, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p != h.preview.RequestID || d != h.preview.ReviewDigest || i != h.preview.Intent {
		return nil, errors.New("mismatch")
	}
	h.applies++
	return &fakeAttendedRequest{h, "apply", "br_" + strings.Repeat("b", 32)}, nil
}
func (h *fakeAttendedHistory) GetPreview(string) (operationhistory.RecordV2, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.preview, nil
}
func (r *fakeAttendedRequest) ID() string     { return r.id }
func (r *fakeAttendedRequest) Started() error { return nil }
func (r *fakeAttendedRequest) TerminalPreview(v dill.PreflightInvocation) error {
	r.history.mu.Lock()
	defer r.history.mu.Unlock()
	if r.history.failPreview {
		return errors.New("synthetic persistence failure")
	}
	r.history.preview.Phase = "terminal"
	r.history.preview.Preview = &v
	r.history.preview.ReviewDigest = operationhistory.Digest("review")
	return nil
}
func (r *fakeAttendedRequest) TerminalApply(dill.OperationInvocation) error {
	if r.history.failApply {
		return errors.New("synthetic persistence failure")
	}
	return nil
}
func (r *fakeAttendedRequest) Delivery(bool) error { return nil }

type fakeCapture struct {
	inv     dill.PreflightInvocation
	stale   atomic.Bool
	applies atomic.Int32
}

func (c *fakeCapture) Intent() (operationhistory.SourceRef, dill.PreflightTarget) {
	return operationhistory.SourceRef{SnapshotDigest: operationhistory.Digest("source"), EvaluatorProfileDigest: operationhistory.Digest("profile"), EvaluatorRefDigest: operationhistory.Digest("evaluator")}, *c.inv.Result.Target
}
func (c *fakeCapture) Revalidate(context.Context) error {
	if c.stale.Load() {
		return errors.New("stale")
	}
	return nil
}
func (c *fakeCapture) Preview(_ context.Context, o dill.PreflightObserver) (dill.PreflightInvocation, error) {
	if err := o.Started(); err != nil {
		return c.inv, err
	}
	return c.inv, o.Terminal(c.inv)
}
func (c *fakeCapture) Apply(_ context.Context, _ *dill.PreflightInvocation, o dill.OperationObserver, w io.Writer) (dill.OperationInvocation, error) {
	c.applies.Add(1)
	if err := o.Started(); err != nil {
		return dill.OperationInvocation{}, err
	}
	if err := o.Terminal(dill.OperationInvocation{ExitCode: -1, Status: "unknown"}); err != nil {
		_, _ = w.Write([]byte("FORBIDDEN_MARKER"))
		return dill.OperationInvocation{}, err
	}
	_, err := w.Write([]byte("FAKE_MARKER"))
	return dill.OperationInvocation{}, err
}

type fakeProducer struct{ capture *fakeCapture }

func (p fakeProducer) Capture(context.Context, string, string) (AttendedCapture, error) {
	return p.capture, nil
}

func fakeFlow(t *testing.T) (*chi.Mux, *fakeAttendedHistory, *fakeCapture, *stackHandler) {
	t.Helper()
	raw, err := os.ReadFile("../dill/testdata/preflight-v1/preview-up-create.json")
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		ExitCode int             `json:"exitCode"`
		Stdout   json.RawMessage `json:"stdout"`
	}
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	compact := new(bytes.Buffer)
	if err = json.Compact(compact, wire.Stdout); err != nil {
		t.Fatal(err)
	}
	inv := dill.ObservePreflightInvocation(compact.Bytes(), &wire.ExitCode)
	if inv.Result == nil {
		t.Fatal("fixture invalid")
	}
	cap := &fakeCapture{inv: inv}
	history := &fakeAttendedHistory{}
	runner := dill.New("synthetic")
	runner.Profile = "alpha"
	ag := agent.NewLocalAgent(&secretRouteEngine{}, runner)
	st, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Create("synthetic", "synthetic"); err != nil {
		t.Fatal(err)
	}
	h := &stackHandler{attended: &AttendedContext{producer: fakeProducer{cap}}, attendedHistory: history, agent: ag, store: st, deployTimeout: time.Second}
	authn, err := auth.New(auth.Config{User: "operator", Password: "password", APIToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(authn.Middleware)
	r.Post("/api/stacks/{name}/operations/preview", h.attendedPreview)
	r.Post("/api/stacks/{name}/up", h.up)
	return r, history, cap, h
}
func fakeRequest(r *chi.Mux, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
func fakeReview(t *testing.T, r *chi.Mux) string {
	t.Helper()
	w := fakeRequest(r, "/api/stacks/synthetic/operations/preview", `{"action":"up"}`)
	if w.Code != 200 {
		t.Fatalf("preview %d %s", w.Code, w.Body.String())
	}
	var v struct {
		ID     string `json:"requestId"`
		Digest string `json:"reviewDigest"`
		Token  string `json:"confirmationToken"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{"requestId": v.ID, "reviewDigest": v.Digest, "confirmationToken": v.Token})
	return string(b)
}
func TestFakeAttendedOneUseConcurrentAndStale(t *testing.T) {
	r, _, cap, _ := fakeFlow(t)
	body := fakeReview(t, r)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- fakeRequest(r, "/api/stacks/synthetic/up", body).Code }()
	}
	wg.Wait()
	close(codes)
	ok, conflict := 0, 0
	for code := range codes {
		if code == 200 {
			ok++
		}
		if code == 409 {
			conflict++
		}
	}
	if ok != 1 || conflict != 1 || cap.applies.Load() != 1 {
		t.Fatalf("ok=%d conflict=%d applies=%d", ok, conflict, cap.applies.Load())
	}
	body = fakeReview(t, r)
	cap.stale.Store(true)
	if w := fakeRequest(r, "/api/stacks/synthetic/up", body); w.Code != 409 {
		t.Fatal(w.Code)
	}
	cap.stale.Store(false)
	if w := fakeRequest(r, "/api/stacks/synthetic/up", body); w.Code != 409 {
		t.Fatal("stale token reused", w.Code)
	}
}
func TestFakeAttendedMismatchAndRestartConsumeAuthority(t *testing.T) {
	r, _, cap, h := fakeFlow(t)
	body := fakeReview(t, r)
	var request map[string]string
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatal(err)
	}
	request["reviewDigest"] = operationhistory.Digest("swapped")
	wrong, _ := json.Marshal(request)
	if w := fakeRequest(r, "/api/stacks/synthetic/up", string(wrong)); w.Code != 409 {
		t.Fatal("mismatch accepted", w.Code)
	}
	if w := fakeRequest(r, "/api/stacks/synthetic/up", body); w.Code != 409 {
		t.Fatal("mismatched token reused", w.Code)
	}
	body = fakeReview(t, r)
	// Simulate a process restart by replacing only the in-memory authority.
	h.attended = &AttendedContext{producer: fakeProducer{cap}}
	if w := fakeRequest(r, "/api/stacks/synthetic/up", body); w.Code != 409 {
		t.Fatal("restart revived token", w.Code)
	}
	if cap.applies.Load() != 0 {
		t.Fatal("unexpected apply")
	}
}
func TestFakeAttendedPersistenceFailureSuppressesMarker(t *testing.T) {
	r, h, cap, _ := fakeFlow(t)
	h.failPreview = true
	if w := fakeRequest(r, "/api/stacks/synthetic/operations/preview", `{"action":"up"}`); w.Code != 503 {
		t.Fatal(w.Code)
	}
	if cap.applies.Load() != 0 {
		t.Fatal("apply after preview failure")
	}
	h.failPreview = false
	body := fakeReview(t, r)
	h.failApply = true
	w := fakeRequest(r, "/api/stacks/synthetic/up", body)
	if strings.Contains(w.Body.String(), "FORBIDDEN_MARKER") || strings.Contains(w.Body.String(), "FAKE_MARKER") {
		t.Fatal("marker escaped failed terminal persistence")
	}
	if w := fakeRequest(r, "/api/stacks/synthetic/up", body); w.Code != 409 {
		t.Fatal("failed token reused", w.Code)
	}
}
