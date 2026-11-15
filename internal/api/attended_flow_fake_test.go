package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
func TestFakeAttendedDecoderValid256RowReview(t *testing.T) {
	r, _, capture, _ := fakeFlow(t)
	result := *capture.inv.Result
	result.Coverage.Containers.Count = 255
	result.Coverage.Volumes.Count = 0
	result.Resources = []dill.PreflightResource{}
	for i := 0; i < 255; i++ {
		name := fmt.Sprintf("synthetic_service_%03d", i)
		result.Resources = append(result.Resources, dill.PreflightResource{Kind: "container", Name: name, Identity: name, IdentityKind: "planned-name", Presence: "absent", Action: "create", Retention: "not-applicable"})
	}
	result.Resources = append(result.Resources, dill.PreflightResource{Kind: "network", Name: "synthetic", Identity: "synthetic", IdentityKind: "planned-name", Presence: "absent", Action: "ensure", Retention: "preserve"})
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	capture.inv = dill.ObservePreflightInvocation(b, capture.inv.ProcessExit)
	if capture.inv.Result == nil {
		t.Fatal("256-row decoder-valid fixture rejected")
	}
	w := fakeRequest(r, "/api/stacks/synthetic/operations/preview", `{"action":"up"}`)
	if w.Code != 200 {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Review operationhistory.RecordV2 `json:"review"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Review.Preview == nil || len(response.Review.Preview.Result.Resources) != 256 || response.Review.Preview.Result.Resources[254].Name != "synthetic_service_254" {
		t.Fatal("review rows truncated or altered")
	}
}

func TestFakeAttendedAllFivePositiveRoutes(t *testing.T) {
	for _, action := range []string{"up", "start", "restart", "stop", "down"} {
		t.Run(action, func(t *testing.T) {
			r, _, capture, _ := fakeFlow(t)
			file := "preview-up-create.json"
			if action == "start" || action == "restart" {
				file = "preview-empty-start.json"
			}
			if action == "stop" || action == "down" {
				file = "preview-down-absent.json"
			}
			data, err := os.ReadFile("../dill/testdata/preflight-v1/" + file)
			if err != nil {
				t.Fatal(err)
			}
			var frame struct {
				ExitCode int                  `json:"exitCode"`
				Stdout   dill.PreflightResult `json:"stdout"`
			}
			if err := json.Unmarshal(data, &frame); err != nil {
				t.Fatal(err)
			}
			frame.Stdout.Operation = action
			payload, err := json.Marshal(frame.Stdout)
			if err != nil {
				t.Fatal(err)
			}
			capture.inv = dill.ObservePreflightInvocation(payload, &frame.ExitCode)
			if capture.inv.Result == nil {
				t.Fatal("synthetic action evidence rejected")
			}
			preview := fakeRequest(r, "/api/stacks/synthetic/operations/preview", `{"action":"`+action+`"}`)
			if preview.Code != 200 {
				t.Fatalf("preview: %d %s", preview.Code, preview.Body.String())
			}
			var result struct {
				ID     string `json:"requestId"`
				Digest string `json:"reviewDigest"`
				Token  string `json:"confirmationToken"`
			}
			if err := json.Unmarshal(preview.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			confirmation, _ := json.Marshal(map[string]string{"requestId": result.ID, "reviewDigest": result.Digest, "confirmationToken": result.Token})
			w := fakeRequest(r, "/api/stacks/synthetic/"+action, string(confirmation))
			if w.Code != 200 || capture.applies.Load() != 1 || !strings.Contains(w.Body.String(), "FAKE_MARKER") {
				t.Fatalf("confirmation: %d %s", w.Code, w.Body.String())
			}
		})
	}
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
func TestFakeAttendedSwappedContextConsumesToken(t *testing.T) {
	for _, mode := range []string{"action", "stack", "source", "target"} {
		t.Run(mode, func(t *testing.T) {
			r, _, capture, _ := fakeFlow(t)
			body := fakeReview(t, r)
			path := "/api/stacks/synthetic/up"
			switch mode {
			case "action":
				path = "/api/stacks/synthetic/stop"
			case "stack":
				path = "/api/stacks/other/up"
			case "source":
				capture.swapSource.Store(true)
			case "target":
				capture.swapTarget.Store(true)
			}
			if w := fakeRequest(r, path, body); w.Code != 409 {
				t.Fatalf("swapped %s accepted: %d", mode, w.Code)
			}
			capture.swapSource.Store(false)
			capture.swapTarget.Store(false)
			if w := fakeRequest(r, "/api/stacks/synthetic/up", body); w.Code != 409 {
				t.Fatalf("token reused: %d", w.Code)
			}
			if capture.applies.Load() != 0 {
				t.Fatal("unexpected apply")
			}
		})
	}
}

func TestFakeAttendedSwappedActorConsumesToken(t *testing.T) {
	r, _, capture, h := fakeFlow(t)
	body := fakeReview(t, r)
	other, err := auth.New(auth.Config{User: "other", Password: "password", APIToken: "other-token"})
	if err != nil {
		t.Fatal(err)
	}
	otherRouter := chi.NewRouter()
	otherRouter.Use(other.Middleware)
	otherRouter.Post("/api/stacks/{name}/up", h.up)
	req := httptest.NewRequest("POST", "/api/stacks/synthetic/up", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer other-token")
	w := httptest.NewRecorder()
	otherRouter.ServeHTTP(w, req)
	if w.Code != 409 {
		t.Fatalf("swapped actor: %d", w.Code)
	}
	if again := fakeRequest(r, "/api/stacks/synthetic/up", body); again.Code != 409 || capture.applies.Load() != 0 {
		t.Fatal("swapped actor token reused")
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
