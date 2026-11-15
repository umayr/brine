package api

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/operationhistory"
)

// In configured mode, deny all unreviewed mutations and legacy evaluator
// routes. This deliberately sacrifices editing rather than permitting a child.
func attendedAPIGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			parts := strings.Split(strings.Trim(path, "/"), "/")
			allowed := r.Method == http.MethodPost && len(parts) == 4 && parts[0] == "api" && parts[1] == "stacks" && attendedAction(parts[3])
			allowed = allowed || r.Method == http.MethodPost && len(parts) == 5 && parts[0] == "api" && parts[1] == "stacks" && parts[3] == "operations" && parts[4] == "preview"
			if !allowed {
				writeError(w, http.StatusServiceUnavailable, "unreviewed operation unavailable in attended mode")
				return
			}
		} else if strings.HasSuffix(path, "/plan") || strings.HasSuffix(path, "/validate") || strings.Contains(path, "/secret-bindings") {
			writeError(w, http.StatusServiceUnavailable, "legacy operation unavailable in attended mode")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func attendedAction(s string) bool {
	switch s {
	case "up", "start", "restart", "stop", "down":
		return true
	}
	return false
}

// Only a single closed object of required string fields is accepted. Token
// parsing rejects duplicate keys, coercion, trailing JSON and oversized input.
func attendedFields(r *http.Request, keys ...string) (map[string]string, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(body) > 4096 {
		return nil, errors.New("invalid body")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	t, err := dec.Token()
	if err != nil || t != json.Delim('{') {
		return nil, errors.New("invalid body")
	}
	allowed := map[string]bool{}
	for _, key := range keys {
		allowed[key] = true
	}
	values := map[string]string{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, errors.New("invalid body")
		}
		key, ok := t.(string)
		if !ok || !allowed[key] {
			return nil, errors.New("invalid body")
		}
		if _, exists := values[key]; exists {
			return nil, errors.New("invalid body")
		}
		var raw json.RawMessage
		if dec.Decode(&raw) != nil || len(raw) == 0 || raw[0] != '"' {
			return nil, errors.New("invalid body")
		}
		var value string
		if json.Unmarshal(raw, &value) != nil || len(value) > 1024 {
			return nil, errors.New("invalid body")
		}
		values[key] = value
	}
	if t, err = dec.Token(); err != nil || t != json.Delim('}') {
		return nil, errors.New("invalid body")
	}
	if dec.Decode(&struct{}{}) != io.EOF || len(values) != len(keys) || dec.InputOffset() > 4096 {
		return nil, errors.New("invalid body")
	}
	return values, nil
}

type previewObserver struct{ request attendedRequest }

func (o previewObserver) Started() error { return o.request.Started() }
func (o previewObserver) Terminal(v dill.PreflightInvocation) error {
	return o.request.TerminalPreview(v)
}

type applyObserver struct {
	request  attendedRequest
	terminal bool
}

func (o *applyObserver) Started() error { return o.request.Started() }
func (o *applyObserver) Terminal(v dill.OperationInvocation) error {
	if err := o.request.TerminalApply(v); err != nil {
		return err
	}
	o.terminal = true
	return nil
}

type durableMarkerWriter struct {
	observer *applyObserver
	out      io.Writer
}

func (w durableMarkerWriter) Write(p []byte) (int, error) {
	if !w.observer.terminal {
		return 0, errors.New("terminal history not durable")
	}
	return w.out.Write(p)
}

func attendedAuthenticated(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !auth.Authenticated(r) {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return false
	}
	return true
}
func unavailableProducer(p AttendedProducer) bool {
	if p == nil {
		return true
	}
	v := reflect.ValueOf(p)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	}
	return false
}
func attendedActor(r *http.Request) string {
	return operationhistory.Digest("brine-authenticated-user-v1\x00" + auth.Identity(r))
}
func attendedBinding(i operationhistory.BoundIntent) dill.PreflightBinding {
	t := i.ExpectedTarget
	return dill.PreflightBinding{Stack: i.Stack, Operation: i.Action, Engine: t.Engine, EndpointDigest: t.EndpointDigest, QualifiedIdentityDigest: t.QualifiedIdentityDigest}
}

func (h *stackHandler) attendedPreview(w http.ResponseWriter, r *http.Request) {
	if !attendedAuthenticated(w, r) {
		return
	}
	if h.attended == nil || h.attendedHistory == nil || h.store == nil || unavailableProducer(h.attended.producer) {
		writeError(w, http.StatusServiceUnavailable, "attended preview unavailable")
		return
	}
	fields, err := attendedFields(r, "action")
	if err != nil || !attendedAction(fields["action"]) {
		writeError(w, http.StatusBadRequest, "invalid preview request")
		return
	}
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	if _, err := h.store.Get(name); err != nil {
		writeError(w, http.StatusNotFound, "stack not found")
		return
	}
	capture, err := h.attended.producer.Capture(r.Context(), name, fields["action"])
	if err != nil || capture == nil {
		writeError(w, http.StatusServiceUnavailable, "attended preview unavailable")
		return
	}
	source, target := capture.Intent()
	intent := operationhistory.BoundIntent{Action: fields["action"], Stack: name, Actor: attendedActor(r), Source: source, ExpectedTarget: target}
	request, err := h.attendedHistory.BeginPreview(intent)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "preview history unavailable")
		return
	}
	w.Header().Set("X-Brine-Request-ID", request.ID())
	w.Header().Set("Location", "/api/operations/"+request.ID())
	inv, err := capture.Preview(r.Context(), previewObserver{request})
	if err != nil || dill.CheckPreflightBinding(&inv, attendedBinding(intent)) != nil {
		writeError(w, http.StatusServiceUnavailable, "preview unavailable; inspect request before a new attempt")
		return
	}
	stored, err := h.attendedHistory.GetPreview(request.ID())
	if err != nil || stored.Persistence != "durable" || stored.Phase != "terminal" || stored.ReviewDigest == "" || stored.Preview == nil {
		writeError(w, http.StatusServiceUnavailable, "preview history unavailable")
		return
	}
	// Only the stored validated preview is retained. Lookup cannot reconstruct a token.
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		writeError(w, http.StatusServiceUnavailable, "confirmation unavailable")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(secret[:])
	now := time.Now()
	h.attended.mu.Lock()
	if h.attended.pending == nil {
		h.attended.pending = make(map[string]attendedReview)
	}
	for k, v := range h.attended.pending {
		if now.After(v.expires) {
			delete(h.attended.pending, k)
		}
	}
	if len(h.attended.pending) >= 128 {
		h.attended.mu.Unlock()
		writeError(w, http.StatusServiceUnavailable, "confirmation capacity unavailable")
		return
	}
	h.attended.pending[token] = attendedReview{intent: intent, previewID: request.ID(), digest: stored.ReviewDigest, invocation: *stored.Preview, capture: capture, expires: now.Add(15 * time.Minute)}
	h.attended.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"requestId": request.ID(), "reviewDigest": stored.ReviewDigest, "confirmationToken": token, "review": stored})
}

func (h *stackHandler) attendedApply(w http.ResponseWriter, r *http.Request, action string) {
	if !attendedAuthenticated(w, r) {
		return
	}
	fields, err := attendedFields(r, "requestId", "reviewDigest", "confirmationToken")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid confirmation request")
		return
	}
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	if h.attendedHistory == nil || unavailableProducer(h.attended.producer) {
		writeError(w, http.StatusServiceUnavailable, "confirmation unavailable")
		return
	}
	h.attended.mu.Lock()
	review, found := h.attended.pending[fields["confirmationToken"]]
	if found {
		delete(h.attended.pending, fields["confirmationToken"])
	}
	h.attended.mu.Unlock()
	if !found || time.Now().After(review.expires) || review.intent.Actor != attendedActor(r) || review.intent.Stack != name || review.intent.Action != action || review.previewID != fields["requestId"] || review.digest != fields["reviewDigest"] {
		writeError(w, http.StatusConflict, "confirmation invalid; obtain a fresh preview")
		return
	}
	if review.capture.Revalidate(r.Context()) != nil {
		writeError(w, http.StatusConflict, "review context changed; obtain a fresh preview")
		return
	}
	source, target := review.capture.Intent()
	if source != review.intent.Source || target != review.intent.ExpectedTarget {
		writeError(w, http.StatusConflict, "review context changed; obtain a fresh preview")
		return
	}
	request, err := h.attendedHistory.BeginApply(review.intent, review.previewID, review.digest)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "apply history unavailable; inspect before a new attempt")
		return
	}
	w.Header().Set("X-Brine-Request-ID", request.ID())
	w.Header().Set("Location", "/api/operations/"+request.ID())
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fw := newFlushWriter(w)
	fw.flush()
	ctx, cancel := httpx.Detached(r.Context(), h.deployTimeout)
	defer cancel()
	observer := &applyObserver{request: request}
	_, _ = review.capture.Apply(ctx, &review.invocation, observer, durableMarkerWriter{observer: observer, out: fw})
	_ = request.Delivery(fw.err != nil || r.Context().Err() != nil)
}
