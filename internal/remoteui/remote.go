// Package remoteui provides an opt-in, read-only remote host UI. It does not
// implement a transport, registry, agent protocol, or signing key.
package remoteui

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/umayr/brine/internal/auth"
)

// Backend must enforce the authoritative agent protocol, identity pinning,
// ownership and all limits independently of this presentation layer.
// Implementations must not retain reveal values or log sensitive content.
type Backend interface {
	Hosts(context.Context) ([]Host, error)
	Inventory(context.Context, string) ([]Container, error)
	Inspect(context.Context, string, string) (Inspection, error)
	Logs(context.Context, string, string, int, bool) (LogStream, error)
	// Reveal must create a fresh signed, one-use grant only after this explicit
	// foreground confirmation, and must never retry it. The agent verifies the grant.
	Reveal(context.Context, string, string, Confirmation) ([]Env, error)
	// Audit receives only an operation and outcome, never identifiers or values.
	Audit(context.Context, string, bool)
	// BeginRevealAudit must durably record intent before signing and return an
	// opaque correlation ID. It must fail if durability cannot be guaranteed.
	BeginRevealAudit(context.Context, RevealIntent) (string, error)
	// FinishRevealAudit durably records outcome, without values or grant data.
	FinishRevealAudit(context.Context, string, bool) error
}

// ReadLeaseBackend pins the complete registry record for a request. Its lease
// must remain valid until release and exclude all cooperating registry writers.
// The returned context carries the verified connection used by Backend reads;
// release cancels streams and closes that connection before unlocking.
type ReadLeaseBackend interface {
	AcquireRead(context.Context, string) (context.Context, func() error, error)
}

type Host struct{ ID, Name, State string }

type Container struct{ ID, Name, Stack, Service, State, Image string }

// IDPrefix is a display hint only; requests always use the complete ID.
func (c Container) IDPrefix() string {
	if fullID(c.ID) {
		return c.ID[:12]
	}
	return "Invalid ID"
}

// Inspection is the bounded v1 normalized profile, not raw engine inspect data.
// Nullable fields use pointers so unavailable values remain distinct from zero.
type Inspection struct {
	ID, Name, Stack, Service, ObservedAt string
	Image, ImageID, State                string
	CreatedAt, User, RestartPolicy       *string
	Command, EnvKeys, Networks           []string
	Labels                               map[string]string
	Ports                                []InspectionPort
	Mounts                               []InspectionMount
	Health                               InspectionHealth
	Resources                            InspectionResources
	Security                             InspectionSecurity
}

type InspectionPort struct {
	Protocol, HostIP        string
	HostPort, ContainerPort int
}
type InspectionMount struct {
	Type, Source, Destination string
	ReadOnly                  bool
}
type InspectionHealth struct {
	Status *string
	Test   []string
}
type InspectionResources struct {
	MemoryBytes, CPUNano, PidsLimit *int64
}
type InspectionSecurity struct {
	Privileged                    *bool
	CapAdd                        []string
	NetworkMode, PIDMode, IPCMode *string
}

// Confirmation is created only after cookie authentication, CSRF middleware,
// and the explicit per-container confirmation. The adapter must revalidate the
// foreground session before signing a one-use agent grant.
type Confirmation struct{ Admin string }

// RevealIntent contains identifiers only, never environment values or grants.
type RevealIntent struct{ Admin, HostID, ContainerID string }

type Env struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// LogStream must translate and validate agent frames without inventing an end.
// Next must unblock when ctx is cancelled; EOF without end means incomplete.
type LogStream interface {
	Next(context.Context) (LogEvent, error)
	Close() error
}
type LogEvent struct {
	Type   string `json:"type"`
	Stream string `json:"stream,omitempty"`
	Bytes  []byte `json:"bytesBase64,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type Handler struct {
	backend      Backend
	auth         *auth.Authenticator
	logDuration  func(bool) time.Duration // test-only override
	readDuration func() time.Duration     // test-only override
}

// New requires the same authenticator's authentication and CSRF middleware
// around the handler. A lease serializes signing with logout and expires.
func New(b Backend, a *auth.Authenticator) (*Handler, error) {
	if b == nil || a == nil || !a.Enabled() {
		return nil, errors.New("remote UI requires backend and enabled authentication")
	}
	return &Handler{backend: b, auth: a}, nil
}

// Protected installs the body limit before authentication/CSRF can parse it.
// This is the only supported production entry point.
func (h *Handler) Protected() http.Handler {
	inner := h.auth.Middleware(http.HandlerFunc(h.serve))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		}
		inner.ServeHTTP(w, r)
	})
}

type page struct {
	CSRF, Host, Container, Error, Notice, ReviewToken string
	Hosts                                             []Host
	Containers                                        []Container
	Inspection                                        *Inspection
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !auth.Authenticated(r) || !auth.CookieAuthenticated(r) || strings.HasPrefix(strings.ToLower(r.Header.Get("Authorization")), "bearer ") {
		http.Error(w, "Interactive admin session required", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodPost {
		if !auth.CookieAuthenticated(r) || strings.HasPrefix(strings.ToLower(r.Header.Get("Authorization")), "bearer ") {
			http.Error(w, "Interactive admin session required", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}
	}
	p := page{CSRF: h.auth.CSRFFor(r), Host: r.FormValue("host"), Container: r.FormValue("container")}
	ctx, cancelSession, ok := h.auth.SessionContext(r)
	if !ok {
		http.Error(w, "Session expired", http.StatusForbidden)
		return
	}
	defer cancelSession()
	if r.Method == http.MethodGet && (r.URL.Path == "/remote/inventory" || r.URL.Path == "/remote/inspect") {
		duration := 30 * time.Second
		if h.readDuration != nil {
			duration = h.readDuration()
		}
		var cancelRead context.CancelFunc
		ctx, cancelRead = context.WithTimeout(ctx, duration)
		defer cancelRead()
	} else if r.Method == http.MethodPost && r.URL.Path == "/remote/logs" {
		follow := r.PostForm.Get("follow") == "yes"
		duration := 15 * time.Second
		if follow {
			duration = 5 * time.Minute
		}
		if h.logDuration != nil {
			duration = h.logDuration(follow)
		}
		var cancelLogs context.CancelFunc
		ctx, cancelLogs = context.WithTimeout(ctx, duration)
		defer cancelLogs()
	}
	// Interrupt a blocked network write when the authenticated session ends or an operation deadline expires.
	// Wait for the deadline callback before clearing it, so it cannot affect a
	// later request on the same connection. Wrappers should support Unwrap.
	controller := http.NewResponseController(w)
	deadlineDone := make(chan struct{})
	poisonConnection := false
	deadlineCtx := ctx
	stopDeadline := context.AfterFunc(deadlineCtx, func() {
		_ = controller.SetWriteDeadline(time.Now())
		close(deadlineDone)
	})
	defer func() {
		if !stopDeadline() {
			<-deadlineDone
		}
		// A failed or cancelled flush may leave buffered bytes. Do not clear the
		// expired write deadline and allow net/http's final flush to send them
		// after the registry lease has been released.
		if !poisonConnection && deadlineCtx.Err() == nil {
			_ = controller.SetWriteDeadline(time.Time{})
		}
	}()
	w = &sessionWriter{ResponseWriter: w, ctx: ctx}
	// The protected dispatcher requires this capability. The lease surrounds
	// rendering and streaming, not merely the backend method call.
	needsLease := r.URL.Path == "/remote/inventory" && r.Method == http.MethodGet && p.Host != "" ||
		r.URL.Path == "/remote/inspect" && r.Method == http.MethodGet && p.Host != "" && fullID(p.Container) ||
		r.URL.Path == "/remote/logs" && r.Method == http.MethodPost && auth.CookieAuthenticated(r) && p.Host != "" && fullID(p.Container)
	if needsLease {
		if required, _ := r.Context().Value(requireWriteDeadlineKey{}).(bool); required {
			if err := controller.SetWriteDeadline(time.Time{}); err != nil {
				http.Error(w, "Streaming unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		if b, ok := h.backend.(ReadLeaseBackend); ok {
			var release func() error
			var err error
			ctx, release, err = b.AcquireRead(ctx, p.Host)
			if err != nil || ctx == nil || release == nil {
				if errors.Is(err, ErrReadCleanup) {
					log.Print("remote read cleanup failed; operator reconciliation required")
				}
				http.Error(w, "Target unavailable", http.StatusServiceUnavailable)
				return
			}
			defer func() {
				// net/http buffers small responses until the handler returns. Flush
				// explicitly while the lease and cancellation deadline are active.
				if err := http.NewResponseController(w).Flush(); err != nil {
					poisonConnection = true
					_ = controller.SetWriteDeadline(time.Now())
					log.Print("remote response flush failed; delivery incomplete")
				}
				if release() != nil {
					log.Print("remote read cleanup failed; operator reconciliation required")
				}
			}()
		}
	}
	switch r.URL.Path {
	case "/remote/session":
		if r.Method != http.MethodGet || !auth.CookieAuthenticated(r) {
			break
		}
		lease, release, ok := h.auth.SubmissionLease(r)
		if !ok {
			http.Error(w, "Session expired", http.StatusForbidden)
			return
		}
		defer release()
		deadline, ok := lease.Deadline()
		if !ok || lease.Err() != nil {
			http.Error(w, "Session expired", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			RemainingMS int64 `json:"remainingMs"`
		}{time.Until(deadline).Milliseconds()})
		return
	case "/remote":
		if r.Method != http.MethodGet {
			break
		}
		hosts, err := h.backend.Hosts(ctx)
		if err != nil {
			p.Error = "Host list unavailable"
		} else {
			p.Hosts = hosts
		}
		h.render(w, p)
		return
	case "/remote/inventory":
		if r.Method != http.MethodGet || p.Host == "" {
			break
		}
		items, err := h.backend.Inventory(ctx, p.Host)
		if err != nil {
			p.Error = "Inventory unavailable or identity changed"
		} else {
			p.Containers = items
		}
		h.render(w, p)
		return
	case "/remote/inspect":
		if r.Method != http.MethodGet || p.Host == "" || !fullID(p.Container) {
			break
		}
		item, err := h.backend.Inspect(ctx, p.Host, p.Container)
		if err != nil {
			p.Error = "Inspection unavailable"
		} else if item.ID != p.Container || !safeText(item.ID) || !safeText(p.Host) {
			p.Error = "Inspection identity mismatch"
		} else if !validInspectionMetadata(item) {
			p.Error = "Inspection unavailable"
		} else {
			p.Inspection = &item
		}
		h.render(w, p)
		return
	case "/remote/logs":
		if r.Method != http.MethodPost || p.Host == "" || !fullID(p.Container) {
			break
		}
		follow := r.PostForm.Get("follow") == "yes"
		stream, err := h.backend.Logs(ctx, p.Host, p.Container, 100, follow)
		h.backend.Audit(ctx, "logs", err == nil)
		if err != nil {
			http.Error(w, "Logs unavailable", http.StatusServiceUnavailable)
			return
		}
		defer stream.Close()
		w.Header().Set("Content-Type", "application/x-ndjson")
		writeLogStream(ctx, w, stream)
		return
	case "/remote/reveal":
		if r.Method != http.MethodPost || !safeText(p.Host) || !fullID(p.Container) || r.PostForm.Get("confirm") != "yes" {
			break
		}
		lease, release, ok := h.auth.SubmissionLease(r)
		if !ok {
			http.Error(w, "Session expired", http.StatusForbidden)
			return
		}
		defer release()
		if lease.Err() != nil {
			http.Error(w, "Session expired", http.StatusForbidden)
			return
		}
		auditID, auditErr := h.backend.BeginRevealAudit(lease, RevealIntent{Admin: auth.Identity(r), HostID: p.Host, ContainerID: p.Container})
		if auditErr != nil || auditID == "" || lease.Err() != nil {
			http.Error(w, "Reveal unavailable", http.StatusServiceUnavailable)
			return
		}
		values, err := h.backend.Reveal(lease, p.Host, p.Container, Confirmation{Admin: auth.Identity(r)})
		data, marshalErr := json.Marshal(values)
		success := err == nil && lease.Err() == nil && marshalErr == nil && len(values) <= 256 && len(data) <= 512<<10
		// Attempt the durable outcome even after disconnect, without retaining values.
		auditCtx, cancelAudit := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		outcomeErr := h.backend.FinishRevealAudit(auditCtx, auditID, success)
		cancelAudit()
		if outcomeErr != nil || !success {
			http.Error(w, "Reveal unavailable", http.StatusServiceUnavailable)
			return
		}
		if lease.Err() != nil {
			http.Error(w, "Session expired", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
		return
	}
	http.Error(w, "Invalid request", http.StatusBadRequest)
}

// sessionWriter suppresses subsequent writes after logout or expiry; it cannot
// recall writes already buffered or in progress when cancellation happens.
type sessionWriter struct {
	http.ResponseWriter
	ctx context.Context
}

func (w *sessionWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.ResponseWriter.Write(p)
}
func (w *sessionWriter) WriteHeader(code int) {
	if w.ctx.Err() == nil {
		w.ResponseWriter.WriteHeader(code)
	}
}
func (w *sessionWriter) FlushError() error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *sessionWriter) Flush()                      { _ = w.FlushError() }
func (w *sessionWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// writeLogStream never treats EOF, errors, cancellation or a local cap as a
// successful agent end. Fixed errors do not reflect untrusted backend text.
func writeLogStream(ctx context.Context, w http.ResponseWriter, s LogStream) {
	writeLogStreamClock(ctx, w, s, time.Now)
}
func writeLogStreamClock(ctx context.Context, w http.ResponseWriter, s LogStream, clock func() time.Time) {
	total, wire, events := 0, 0, 0
	const maxWire = 2 << 20
	var lastHeartbeat time.Time
	lastRate := clock()
	tokens := 256.0
	emit := func(e LogEvent) bool {
		b, err := json.Marshal(e)
		if err != nil {
			return false
		}
		b = append(b, '\n')
		if wire+len(b) > maxWire {
			return false
		}
		wire += len(b)
		if _, err = w.Write(b); err != nil {
			return false
		}
		if f, ok := w.(interface{ FlushError() error }); ok {
			return f.FlushError() == nil
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return true
	}
	for {
		e, err := s.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				emit(LogEvent{Type: "error", Reason: "deadline_or_cancelled"})
			} else if errors.Is(err, io.EOF) {
				emit(LogEvent{Type: "error", Reason: "incomplete"})
			} else {
				emit(LogEvent{Type: "error", Reason: "unavailable"})
			}
			return
		}
		events++
		now := clock()
		tokens += now.Sub(lastRate).Seconds() * 200
		if tokens > 256 {
			tokens = 256
		}
		lastRate = now
		if events > 4096 || tokens < 1 || wire > maxWire-32768 {
			emit(LogEvent{Type: "error", Reason: "truncated"})
			return
		}
		tokens--
		switch e.Type {
		case "chunk":
			if len(e.Bytes) == 0 || len(e.Bytes) > 16<<10 || (e.Stream != "stdout" && e.Stream != "stderr" && e.Stream != "unknown") {
				emit(LogEvent{Type: "error", Reason: "invalid_stream"})
				return
			}
			if total+len(e.Bytes) > 1<<20 {
				emit(LogEvent{Type: "error", Reason: "truncated"})
				return
			}
			total += len(e.Bytes)
			if !emit(LogEvent{Type: "chunk", Stream: e.Stream, Bytes: e.Bytes}) {
				return
			}
		case "heartbeat":
			if !lastHeartbeat.IsZero() && now.Sub(lastHeartbeat) < 20*time.Second {
				emit(LogEvent{Type: "error", Reason: "invalid_stream"})
				return
			}
			lastHeartbeat = now
			if !emit(LogEvent{Type: "heartbeat"}) {
				return
			}
		case "end":
			if e.Reason != "complete" && e.Reason != "duration" && e.Reason != "byte_limit" && e.Reason != "cancelled" {
				emit(LogEvent{Type: "error", Reason: "invalid_stream"})
				return
			}
			emit(LogEvent{Type: "end", Reason: e.Reason})
			return
		default:
			emit(LogEvent{Type: "error", Reason: "unavailable"})
			return
		}
	}
}

// fullID accepts only the canonical full lower-case hexadecimal engine ID.
// Adapters for engines with a different ID format require an explicit contract.
func fullID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Reject malformed names and any unexpected label before rendering anything.
// Other permitted inspection metadata can still contain secrets.
func validInspectionMetadata(i Inspection) bool {
	if len(i.Labels) != 3 || i.Labels["dill.managed"] != "true" || i.Stack == "" || i.Service == "" || i.Labels["dill.stack"] != i.Stack || i.Labels["dill.service"] != i.Service || len(i.EnvKeys) > 256 {
		return false
	}
	for n, key := range i.EnvKeys {
		if key == "" || len(key) > 8192 || !utf8.ValidString(key) || strings.ContainsAny(key, "=\x00") || (n > 0 && i.EnvKeys[n-1] >= key) {
			return false
		}
	}
	return true
}

func safeText(s string) bool {
	if s == "" || len(s) > 4096 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 {
			return false
		}
	}
	return true
}
func (h *Handler) render(w http.ResponseWriter, p page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = view.Execute(w, p)
}

var view = template.Must(template.New("remote").Parse(pageHTML))
