package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/operationhistory"
	"github.com/umayr/brine/internal/store"
)

func (h *stackHandler) beginOperation(ctx context.Context, w http.ResponseWriter, r *http.Request, stack *store.Stack, action string) (context.Context, *operationhistory.Request, bool) {
	if h.history == nil {
		return ctx, nil, true
	}
	runner := h.agent.Dill()
	if !auth.Authenticated(r) || runner == nil || runner.Profile != "alpha" || runner.QualifiedOperation == nil {
		writeError(w, http.StatusUnauthorized, "operation history requires authenticated qualified Alpha")
		return ctx, nil, false
	}
	// Hash only the existing trusted authenticated account identity, never a
	// token, cookie, CSRF value or client-supplied attribution string. This is
	// correlation metadata, not a confidentiality/anonymization claim.
	request, e := h.history.Begin(operationhistory.Intent{Action: action, Stack: stack.Name, Actor: operationhistory.Digest("brine-authenticated-user-v1\x00" + auth.Identity(r)), Entrypoint: stack.Entrypoint, SelectorDigest: operationhistory.Digest("unix://" + strings.TrimPrefix(runner.Socket, "unix://")), ExpectedIdentityDigest: operationhistory.Digest("dill-local-docker-v1\x00" + runner.QualifiedOperation.DaemonID)})
	if e != nil {
		writeError(w, http.StatusServiceUnavailable, "operation history unavailable; no operation launched; inspect before retrying")
		return ctx, nil, false
	}
	w.Header().Set("X-Brine-Request-ID", request.ID())
	w.Header().Set("Location", "/api/operations/"+request.ID())
	return dill.WithOperationObserver(ctx, request), request, true
}
func historyDelivery(request *operationhistory.Request, fw *flushWriter, r *http.Request) {
	if request != nil {
		_ = request.Delivery(fw.err != nil || r.Context().Err() != nil)
	}
}
func (h *stackHandler) operationGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Defense in depth includes synthetic/no-auth mounts, not just main router.
	if !auth.Authenticated(r) {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	id := chi.URLParam(r, "id")
	if !operationhistory.ValidID(id) || h.history == nil {
		writeError(w, http.StatusNotFound, "operation not found")
		return
	}
	record, e := h.history.Get(id)
	if e != nil {
		if errors.Is(e, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "operation not found")
		} else {
			writeError(w, http.StatusServiceUnavailable, "operation history unavailable; inspect private state")
		}
		return
	}
	w.Header().Set("X-Brine-Request-ID", id)
	writeJSON(w, http.StatusOK, record)
}
