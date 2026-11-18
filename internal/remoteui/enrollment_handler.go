package remoteui

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/targetregistry"
)

// EnrollmentObserver must establish a fresh pinned connection using only the
// supplied candidate and verify the agent proof against it. Observe must fail
// closed on any mismatch. It must never discover, adopt, or replace pins.
type EnrollmentObserver interface {
	Observe(context.Context, targetregistry.Record) (targetregistry.Record, error)
}

type EnrollmentHandler struct {
	auth            *auth.Authenticator
	registry        *targetregistry.Registry
	observer        EnrollmentObserver
	approvals       *enrollmentApprovals
	beforeCommit    func()                                                                 // test-only synchronization hook
	approvalContext func(context.Context, time.Time) (context.Context, context.CancelFunc) // test-only cancellation control
	requestDuration func() time.Duration                                                   // test-only override
}

// NewEnrollmentHandler constructs an unmounted handler. The registry and
// observer must remain available for the lifetime of the handler.
func NewEnrollmentHandler(a *auth.Authenticator, r *targetregistry.Registry, o EnrollmentObserver) (*EnrollmentHandler, error) {
	if a == nil || !a.Enabled() || r == nil || o == nil {
		return nil, errors.New("enrollment requires authenticated configuration")
	}
	approvals, err := newEnrollmentApprovals()
	if err != nil {
		return nil, err
	}
	return &EnrollmentHandler{auth: a, registry: r, observer: o, approvals: approvals}, nil
}

// Protected must be mounted without another authentication middleware in front
// of it: the request body limit must precede all auth form parsing.
func (h *EnrollmentHandler) Protected() http.Handler {
	inner := h.auth.Middleware(http.HandlerFunc(h.serveEnrollment))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		duration := 30 * time.Second
		if h.requestDuration != nil {
			duration = h.requestDuration()
		}
		deadline := time.Now().Add(duration)
		ctx, cancel := context.WithDeadline(r.Context(), deadline)
		defer cancel()
		// Install before authentication (which may parse the form or respond).
		// Never clear: net/http still has final response framing to send.
		controller := http.NewResponseController(w)
		if err := controller.SetWriteDeadline(deadline); err != nil {
			panic(http.ErrAbortHandler)
		}
		if err := controller.SetReadDeadline(deadline); err != nil {
			panic(http.ErrAbortHandler)
		}
		r = r.WithContext(ctx)
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		}
		inner.ServeHTTP(w, r)
	})
}

type enrollmentField struct{ Name, Value string }
type enrollmentPage struct {
	CSRF, Token, Error, Notice  string
	Fields, Previous, Candidate []enrollmentField
}

func enrollmentFields(r targetregistry.Record) []enrollmentField {
	v := reflect.ValueOf(r)
	typ := v.Type()
	fields := make([]enrollmentField, 0, v.NumField())
	for i := 0; i < v.NumField(); i++ {
		name := typ.Field(i).Tag.Get("json")
		if strings.Contains(name, ",") { // HTTPS-only fields are not part of the SSH form.
			continue
		}
		fields = append(fields, enrollmentField{name, fmt.Sprint(v.Field(i).Interface())})
	}
	return fields
}

func displayEnrollmentFields(r targetregistry.Record) []enrollmentField {
	fields := enrollmentFields(r)
	for i := range fields {
		fields[i].Value = strings.ReplaceAll(strconv.QuoteToASCII(fields[i].Value), " ", `\x20`)
	}
	return fields
}

func parseEnrollment(r *http.Request) (targetregistry.Record, error) {
	var out targetregistry.Record
	if err := r.ParseForm(); err != nil {
		return out, err
	}
	v := reflect.ValueOf(&out).Elem()
	typ := v.Type()
	if len(r.PostForm) != len(enrollmentFields(out))+1 || len(r.PostForm["_csrf"]) != 1 {
		return out, errors.New("invalid form")
	}
	for i := 0; i < v.NumField(); i++ {
		name := typ.Field(i).Tag.Get("json")
		if strings.Contains(name, ",") {
			continue
		}
		values := r.PostForm[name]
		if len(values) != 1 {
			return out, errors.New("invalid field")
		}
		switch v.Field(i).Kind() {
		case reflect.String:
			v.Field(i).SetString(values[0])
		case reflect.Uint16, reflect.Uint64:
			n, err := strconv.ParseUint(values[0], 10, v.Field(i).Type().Bits())
			if err != nil || strconv.FormatUint(n, 10) != values[0] {
				return out, errors.New("invalid integer")
			}
			v.Field(i).SetUint(n)
		default:
			return out, errors.New("unsupported field")
		}
	}
	return out, out.Validate()
}

func (h *EnrollmentHandler) serveEnrollment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	fail := func(message string, status int) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = enrollmentView.Execute(w, enrollmentPage{Error: message})
	}
	if !auth.Authenticated(r) || !auth.CookieAuthenticated(r) || strings.HasPrefix(strings.ToLower(r.Header.Get("Authorization")), "bearer ") {
		fail("Interactive admin session required", http.StatusForbidden)
		return
	}
	page := enrollmentPage{CSRF: h.auth.CSRFFor(r)}
	render := func() {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = enrollmentView.Execute(w, page)
	}
	if r.URL.Path == "/remote/enrollment" && r.Method == http.MethodGet {
		page.Fields = enrollmentFields(targetregistry.Record{Revision: 1, Port: 22, Generation: 1})
		render()
		return
	}
	if r.Method != http.MethodPost {
		fail("Invalid request", http.StatusBadRequest)
		return
	}
	lease, release, ok := h.auth.SubmissionLease(r)
	if !ok {
		fail("Session expired", http.StatusForbidden)
		return
	}
	defer func() {
		defer release()
		// net/http buffers small responses; retain the authorization lease until
		// the response has been handed to the transport or delivery has failed.
		if err := http.NewResponseController(w).Flush(); err != nil {
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now())
		}
	}()
	cookie, err := r.Cookie(auth.SessionCookie)
	if err != nil {
		fail("Session required", http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/remote/enrollment/review":
		candidate, e := parseEnrollment(r)
		if e != nil {
			fail("Invalid candidate", http.StatusBadRequest)
			return
		}
		snapshot := enrollmentSnapshot{Candidate: candidate}
		previous, e := h.registry.GetContext(lease, candidate.ID)
		if e == nil {
			snapshot.Previous = &previous
		} else if !errors.Is(e, targetregistry.ErrNotFound) {
			page.Error = "Previous record unavailable"
			render()
			return
		}
		if e = snapshot.validate(); e != nil {
			page.Error = "Invalid enrollment revision"
			render()
			return
		}
		observed, e := h.observer.Observe(lease, candidate)
		if e != nil || candidate.Verify(observed) != nil || lease.Err() != nil {
			page.Error = "Pinned observation failed"
			render()
			return
		}
		token, e := h.approvals.issue(cookie.Value, snapshot, time.Now())
		if e != nil {
			page.Error = "Review unavailable"
			render()
			return
		}
		page.Token = token
		page.Candidate = displayEnrollmentFields(snapshot.Candidate)
		if snapshot.Previous != nil {
			page.Previous = displayEnrollmentFields(*snapshot.Previous)
		}
		render()
	case "/remote/enrollment/approve":
		if err := r.ParseForm(); err != nil || len(r.PostForm) != 3 || len(r.PostForm["_csrf"]) != 1 || len(r.PostForm["token"]) != 1 || len(r.PostForm["confirm"]) != 1 || r.PostForm.Get("confirm") != "yes" {
			fail("Invalid approval", http.StatusBadRequest)
			return
		}
		snapshot, ok := h.approvals.consume(cookie.Value, r.PostForm.Get("token"), time.Now())
		if !ok {
			fail("Review expired or invalid", http.StatusForbidden)
			return
		}
		makeApprovalContext := context.WithDeadline
		if h.approvalContext != nil {
			makeApprovalContext = h.approvalContext
		}
		approvalCtx, cancelApproval := makeApprovalContext(lease, snapshot.expires)
		defer cancelApproval()
		observed, e := h.observer.Observe(approvalCtx, snapshot.Candidate)
		if e != nil || approvalCtx.Err() != nil {
			page.Error = "Fresh pinned observation failed"
			render()
			return
		}
		if h.beforeCommit != nil {
			h.beforeCommit()
		}
		if e = commitEnrollment(approvalCtx, h.registry, snapshot, observed); e != nil {
			page.Error = "Enrollment outcome uncertain; reload the registry and reconcile before retrying"
		} else {
			page.Notice = "Enrollment approved"
		}
		render()
	default:
		http.NotFound(w, r)
	}
}

var enrollmentView = template.Must(template.New("enrollment").Parse(`<!doctype html><html><head><title>Enroll remote host</title></head><body><main><h1>Enroll remote host</h1><p>Independently obtain and verify every pin and identity out of band. Do not approve values learned only from this connection.</p><p>Remote inspection metadata and logs may contain secrets. Access to a container engine can grant host-level privileges; enroll only a trusted, isolated target.</p>{{if .Error}}<p role="alert">{{.Error}}</p>{{end}}{{if .Notice}}<p>{{.Notice}}</p>{{end}}{{if .Fields}}<form method="post" action="/remote/enrollment/review"><input type="hidden" name="_csrf" value="{{.CSRF}}">{{range .Fields}}<label>{{.Name}} <input name="{{.Name}}" value="{{.Value}}" required></label><br>{{end}}<button>Review candidate</button></form>{{end}}{{if .Candidate}}{{if .Previous}}<h2>Previous record</h2><dl>{{range .Previous}}<dt>{{.Name}}</dt><dd><code style="white-space:pre-wrap;overflow-wrap:anywhere"><bdi>{{.Value}}</bdi></code></dd>{{end}}</dl>{{else}}<h2>New enrollment</h2>{{end}}<h2>Complete candidate</h2><dl>{{range .Candidate}}<dt>{{.Name}}</dt><dd><code style="white-space:pre-wrap;overflow-wrap:anywhere"><bdi>{{.Value}}</bdi></code></dd>{{end}}</dl><form method="post" action="/remote/enrollment/approve"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="token" value="{{.Token}}"><label><input type="checkbox" name="confirm" value="yes" required> I independently verified every field out of band</label><button>Approve exact records</button></form>{{end}}<nav><a href="/remote">Return to hosts</a> · <a href="/remote/enrollment">Start a new review (re-enter candidate)</a></nav><p>For uncertain outcomes, reconcile the registry before starting another review. A new review never repeats an approval automatically.</p></main></body></html>`))
