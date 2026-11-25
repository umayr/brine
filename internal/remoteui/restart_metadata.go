package remoteui

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/targetregistry"
)

const restartMetadataTimeout = 2 * time.Second

var errRestartMetadata = errors.New("restart metadata unavailable")

type restartMetadataReader interface {
	DiscoverRestartMetadata(context.Context, snapshothistory.RestartDiscoveryRequest) (snapshothistory.RestartDiscoveryPage, error)
}

// restartMetadataAccess is independent of deployment authority and permission
// to open archived configuration. It must check current local metadata access
// for this authenticated actor and exact enrollment on every invocation.
type restartMetadataAccess func(context.Context, archiveReadActor, targetregistry.Record, targetregistry.HistoryIncarnation) error

type restartScopeAuthorizer struct {
	scope string
	check func(context.Context) error
}

func (a restartScopeAuthorizer) AuthorizeSnapshotHistoryRestart(ctx context.Context, scope []byte) error {
	if string(scope) != a.scope || a.check == nil || ctx.Err() != nil {
		return errRestartMetadata
	}
	return a.check(ctx)
}

// protectedRestartMetadata serves only the explicitly configured metadata route.
// A successful page is intent metadata, never a provider outcome, restore
// approval or permission to send.
func (h *Deployment) protectedRestartMetadata(reader restartMetadataReader, access restartMetadataAccess) http.Handler {
	if h == nil || h.auth == nil || !h.auth.Enabled() {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Restart metadata unavailable", http.StatusServiceUnavailable)
		})
	}
	inner := h.auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered := false
		deny := func() {
			http.Error(w, "Restart metadata unavailable", http.StatusServiceUnavailable)
			delivered = http.NewResponseController(w).Flush() == nil
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !auth.Authenticated(r) || !auth.CookieAuthenticated(r) || strings.HasPrefix(strings.ToLower(r.Header.Get("Authorization")), "bearer ") {
			http.Error(w, "Interactive admin session required", http.StatusForbidden)
			return
		}
		actor, ok := h.archiveReadActor(r)
		if !ok || reader == nil || access == nil || h.registry == nil {
			deny()
			return
		}
		if len(r.URL.RawQuery) > 2048 {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}
		query, err := parseRestartMetadataQuery(r)
		if err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}
		session, release, ok := h.auth.SessionContext(r)
		if !ok {
			http.Error(w, "Session expired", http.StatusForbidden)
			return
		}
		defer release()
		ctx, cancel := context.WithTimeout(session, restartMetadataTimeout)
		defer cancel()
		w = &sessionWriter{ResponseWriter: w, ctx: ctx}
		controller := http.NewResponseController(w)
		deadline, _ := ctx.Deadline()
		if controller.SetWriteDeadline(deadline) != nil {
			deny()
			return
		}
		done := make(chan struct{})
		stop := context.AfterFunc(ctx, func() { _ = controller.SetWriteDeadline(time.Now()); close(done) })
		defer func() {
			if !stop() {
				<-done
			}
			if delivered && ctx.Err() == nil {
				_ = controller.SetWriteDeadline(time.Time{})
			} else {
				_ = controller.SetWriteDeadline(time.Now())
			}
		}()
		record, enrollment, lease, err := h.registry.AcquireHistoryRead(ctx, query.host)
		if err != nil {
			deny()
			return
		}
		defer func() {
			if lease.Close() != nil {
				delivered = false
				log.Print("restart metadata enrollment cleanup failed; delivery may be incomplete")
			}
		}()
		if record.HTTPSAddress == "" || enrollment == (targetregistry.HistoryIncarnation{}) {
			deny()
			return
		}
		check := func(ctx context.Context) error {
			if ctx.Err() != nil {
				return errRestartMetadata
			}
			if err := access(ctx, actor, record, enrollment); err != nil || ctx.Err() != nil {
				return errRestartMetadata
			}
			return nil
		}
		scope := hex.EncodeToString(enrollment[:])
		if check(ctx) != nil {
			deny()
			return
		}
		page, err := reader.DiscoverRestartMetadata(ctx, snapshothistory.RestartDiscoveryRequest{ApprovedTargetScope: []byte(scope), Authorizer: restartScopeAuthorizer{scope, check}, After: query.after, Limit: 32})
		if err != nil || len(page.Records) > 32 || len(page.Next) > 1024 || check(ctx) != nil {
			deny()
			return
		}
		// Bound variable-length fields before encoding. The reader owns verification
		// of archive metadata; this boundary owns response size and publication.
		for _, row := range page.Records {
			if len(row.AttemptID) > 128 || len(row.ProviderRequestID) > 128 || len(row.OriginalJournal) > 128 {
				deny()
				return
			}
		}
		var body bytes.Buffer
		if json.NewEncoder(&body).Encode(page) != nil || body.Len() > 64<<10 || check(ctx) != nil {
			deny()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(body.Bytes()); err != nil {
			return
		}
		// Flush before releasing the enrollment lease: net/http otherwise buffers
		// a small successful response until after the handler returns.
		if controller.Flush() != nil || ctx.Err() != nil {
			return
		}
		delivered = true
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		inner.ServeHTTP(w, r)
	})
}

type restartMetadataQuery struct{ host, after string }

func parseRestartMetadataQuery(r *http.Request) (restartMetadataQuery, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return restartMetadataQuery{}, errRestartMetadata
	}
	for key, values := range q {
		if (key != "host" && key != "after") || len(values) != 1 {
			return restartMetadataQuery{}, errRestartMetadata
		}
	}
	host, after := q.Get("host"), q.Get("after")
	if host == "" || len(host) > 128 || len(after) > 1024 {
		return restartMetadataQuery{}, errRestartMetadata
	}
	return restartMetadataQuery{host, after}, nil
}
