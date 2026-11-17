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
	inv         dill.PreflightInvocation
	stale       atomic.Bool
	swapSource  atomic.Bool
	swapTarget  atomic.Bool
	previewHook func()
	applyHook   func()
	applies     atomic.Int32
}

func (c *fakeCapture) Intent() (operationhistory.SourceRef, dill.PreflightTarget) {
	source := operationhistory.SourceRef{SnapshotDigest: operationhistory.Digest("source"), EvaluatorProfileDigest: operationhistory.Digest("profile"), EvaluatorRefDigest: operationhistory.Digest("evaluator")}
	target := *c.inv.Result.Target
	if c.swapSource.Load() {
		source.SnapshotDigest = operationhistory.Digest("changed")
	}
	if c.swapTarget.Load() {
		target.EndpointDigest = operationhistory.Digest("changed")
	}
	return source, target
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
	if c.previewHook != nil {
		c.previewHook()
	}
	return c.inv, o.Terminal(c.inv)
}
func (c *fakeCapture) Apply(_ context.Context, _ *dill.PreflightInvocation, o dill.OperationObserver, w io.Writer) (dill.OperationInvocation, error) {
	c.applies.Add(1)
	if err := o.Started(); err != nil {
		return dill.OperationInvocation{}, err
	}
	if c.applyHook != nil {
		c.applyHook()
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
	r.Post("/api/stacks/{name}/start", h.start)
	r.Post("/api/stacks/{name}/restart", h.restart)
	r.Post("/api/stacks/{name}/stop", h.stop)
	r.Post("/api/stacks/{name}/down", h.down)
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
