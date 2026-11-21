package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/engine"
	gitpkg "github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/operationhistory"
	"github.com/umayr/brine/internal/pkl"
	"github.com/umayr/brine/internal/store"
)

type stackHandler struct {
	history       *operationhistory.Store
	agent         agent.Agent
	store         store.StackStore
	git           *gitpkg.Runner
	runtimeEngine string
	deployTimeout time.Duration
}

// Defense in depth for mounted API routers: a configured qualified client must
// never run through an unauthenticated synthetic or development route.
func (h *stackHandler) qualifiedActionAllowed(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if h.agent != nil && h.agent.Dill() != nil && h.agent.Dill().QualifiedOperationConfigured() && !auth.Authenticated(r) {
		writeError(w, http.StatusUnauthorized, "qualified operations require authentication")
		return false
	}
	return true
}

type stackResponse struct {
	*store.Stack
	PklContent *string `json:"pkl_content,omitempty"` // a successful read can return an empty file
}

// stackReadMetadata reports the effective entrypoint without changing stored metadata.
func stackReadMetadata(stack *store.Stack) *store.Stack {
	if stack == nil || stack.Entrypoint != "" {
		return stack
	}
	metadata := *stack
	metadata.Entrypoint = stack.EffectiveEntrypoint()
	return &metadata
}

func (h *stackHandler) list(w http.ResponseWriter, r *http.Request) {
	stacks, err := h.store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Stack list unavailable; check local stack storage")
		return
	}
	metadata := slices.Clone(stacks)
	for i, stack := range stacks {
		metadata[i] = stackReadMetadata(stack)
	}
	writeJSON(w, http.StatusOK, metadata)
}

func writeStackLookupError(w http.ResponseWriter, name string, err error) {
	if errors.Is(err, store.ErrStackNotFound) || store.ValidateStackName(name) != nil {
		writeError(w, http.StatusNotFound, "stack not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "stack metadata unavailable; check local stack storage")
}

func (h *stackHandler) get(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	stack, err := h.store.Get(name)
	if err != nil {
		writeStackLookupError(w, name, err)
		return
	}
	pkl, err := h.store.ReadPkl(name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to read stack configuration")
		return
	}
	writeJSON(w, http.StatusOK, stackResponse{Stack: stackReadMetadata(stack), PklContent: &pkl})
}

type createRequest struct {
	Name       string `json:"name"`
	PklContent string `json:"pkl_content"`
}

func (h *stackHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.PklContent == "" {
		writeError(w, http.StatusBadRequest, "name and pkl_content are required")
		return
	}
	defer h.lockStack(req.Name)()
	stack, err := h.store.Create(req.Name, pkl.WithStackName(req.PklContent, req.Name))
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "already exists") {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, stack)
}

type updateRequest struct {
	PklContent string `json:"pkl_content"`
}

func (h *stackHandler) update(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	var req updateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	stack, err := h.store.Update(name, req.PklContent)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			writeError(w, http.StatusNotFound, "stack not found")
		case errors.Is(err, store.ErrReadOnly):
			writeError(w, http.StatusForbidden, "stack is read-only")
		default:
			writeError(w, http.StatusInternalServerError, "unable to update stack")
		}
		return
	}
	writeJSON(w, http.StatusOK, stack)
}

const maxStackActionOptionsBytes = 4096

// decodeStackActionOption accepts an omitted body, an empty object, or an
// object with exactly one canonical option of the expected scalar type.
// Unknown or repeated keys must not silently fall back to default options.
func decodeStackActionOption[T bool | string](w http.ResponseWriter, r *http.Request, option string, limit int64) (T, bool) {
	var zero, result T
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil || !utf8.Valid(body) {
		return zero, false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err == io.EOF {
		return zero, true
	}
	if err != nil || tok != json.Delim('{') {
		return zero, false
	}

	tok, err = dec.Token()
	if key, ok := tok.(string); err == nil && ok && key == option {
		value, valueErr := dec.Token()
		optionValue, ok := value.(T)
		if valueErr != nil || !ok {
			return zero, false
		}
		result = optionValue
		tok, err = dec.Token()
	}
	if err != nil || tok != json.Delim('}') {
		return zero, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return zero, false
	}
	return result, true
}

func (h *stackHandler) delete(w http.ResponseWriter, r *http.Request) {
	if h.agent != nil && h.agent.Dill() != nil && h.agent.Dill().QualifiedOperationConfigured() {
		writeError(w, http.StatusConflict, "qualified stack deletion is deferred: preserve source and operation recovery context; use explicit Down and inspect its receipt")
		return
	}
	removeContainers, ok := decodeStackActionOption[bool](w, r, "remove_containers", maxStackActionOptionsBytes)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	if removeContainers {
		ctx, cancel := httpx.Detached(r.Context(), h.deployTimeout)
		defer cancel()
		var out bytes.Buffer
		if err := h.agent.Dill().Down(ctx, stack.PklPath, &out); err != nil {
			msg := strings.TrimSpace(out.String())
			if msg == "" {
				msg = err.Error()
			}
			writeError(w, http.StatusBadGateway, "stop/remove containers: "+msg)
			return
		}
	}

	if err := h.store.Delete(name); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// lifecycle centralizes the shared authorization, locking, history, and streaming
// steps while keeping action-specific engine checks explicit at the call sites.
func (h *stackHandler) lifecycle(w http.ResponseWriter, r *http.Request, action string, checkEngine bool, run func(context.Context, string, io.Writer) error) {
	if !h.qualifiedActionAllowed(w, r) {
		return
	}
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	stack, err := h.store.Get(name)
	if err != nil {
		writeStackLookupError(w, name, err)
		return
	}
	if checkEngine && !h.ensureRuntimeEngine(w, stack) {
		return
	}
	ctx, cancel := httpx.Detached(r.Context(), h.deployTimeout)
	defer cancel()
	ctx, request, ok := h.beginOperation(ctx, w, r, stack, action)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if action == "up" {
		w.Header().Set("X-Content-Type-Options", "nosniff")
	}
	w.WriteHeader(http.StatusOK)
	fw := newFlushWriter(w)
	defer historyDelivery(request, fw, r)
	if request != nil {
		fw.flush()
	} // Publish stable lookup ID before discovery.
	writeActionStatus(fw, action, run(ctx, stack.PklPath, fw))
}

func (h *stackHandler) up(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, "up", true, func(ctx context.Context, path string, out io.Writer) error {
		return h.agent.Dill().Up(ctx, path, out)
	})
}

func (h *stackHandler) down(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, "down", false, func(ctx context.Context, path string, out io.Writer) error {
		return h.agent.Dill().Down(ctx, path, out)
	})
}

func (h *stackHandler) stop(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, "stop", false, func(ctx context.Context, path string, out io.Writer) error {
		return h.agent.Dill().Stop(ctx, path, out)
	})
}

func (h *stackHandler) start(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, "start", true, func(ctx context.Context, path string, out io.Writer) error {
		return h.agent.Dill().Start(ctx, path, out)
	})
}

func (h *stackHandler) restart(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, "restart", true, func(ctx context.Context, path string, out io.Writer) error {
		return h.agent.Dill().Restart(ctx, path, out)
	})
}

func (h *stackHandler) teardown(w http.ResponseWriter, r *http.Request) {
	if !h.qualifiedActionAllowed(w, r) {
		return
	}
	force, ok := decodeStackActionOption[bool](w, r, "force", maxStackActionOptionsBytes)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if _, err := h.store.ReadPkl(stack.Name); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "teardown requires a safe recipe; stop and down remain available")
		return
	}
	if !h.ensureRuntimeEngine(w, stack) {
		return
	}
	ctx, cancel := httpx.Detached(r.Context(), h.deployTimeout)
	defer cancel()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fw := newFlushWriter(w)
	writeActionStatus(fw, "teardown", h.agent.Dill().Teardown(ctx, stack.PklPath, force, fw))
}

func (h *stackHandler) plan(w http.ResponseWriter, r *http.Request) {
	if !h.qualifiedActionAllowed(w, r) {
		return
	}
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	stack, err := h.store.Get(name)
	if err != nil {
		writeStackLookupError(w, name, err)
		return
	}
	if !h.ensureRuntimeEngine(w, stack) {
		return
	}
	result, err := h.agent.Dill().Plan(r.Context(), stack.PklPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *stackHandler) validate(w http.ResponseWriter, r *http.Request) {
	if !h.qualifiedActionAllowed(w, r) {
		return
	}
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	stack, err := h.store.Get(name)
	if err != nil {
		writeStackLookupError(w, name, err)
		return
	}
	if err := h.agent.Dill().Validate(r.Context(), stack.PklPath); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"valid": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true})
}

func (h *stackHandler) containers(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	all, err := h.agent.Engine().ListContainers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Stack container inventory unavailable; check the local container engine and retry")
		return
	}
	filtered := []engine.ContainerInfo{}
	for _, c := range all {
		if c.Stack == name {
			filtered = append(filtered, c)
		}
	}
	writeJSON(w, http.StatusOK, filtered)
}

// --- Stack file management ---

func (h *stackHandler) listFiles(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	files, err := h.store.Files(name)
	if err != nil {
		writeError(w, http.StatusNotFound, "Stack file list unavailable; check the stack name and local workspace storage")
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (h *stackHandler) getFile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	filename := chi.URLParam(r, "filename")
	content, err := h.store.ReadFile(name, filename)
	if err != nil {
		writeError(w, http.StatusNotFound, "Stack file unavailable; check the file name and local workspace storage")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"filename": filename, "content": content})
}

func (h *stackHandler) writeFile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	filename := chi.URLParam(r, "filename")
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := h.store.WriteFile(name, filename, body.Content); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *stackHandler) deleteFile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	filename := chi.URLParam(r, "filename")
	if err := h.store.DeleteFile(name, filename); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *stackHandler) setEntrypoint(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	var body struct {
		Filename string `json:"filename"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := h.store.SetEntrypoint(name, body.Filename); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Git operations ---

// createFromGit clones a remote repo and registers the stack.
func (h *stackHandler) createFromGit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string          `json:"name"`
		Git        store.GitSource `json:"git"`
		Entrypoint string          `json:"entrypoint"` // filename within folder, default compose.pkl
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == "" || req.Git.URL == "" {
		writeError(w, http.StatusBadRequest, "name and git.url are required")
		return
	}
	if req.Git.Branch == "" {
		req.Git.Branch = "main"
	}

	repoDir, err := h.store.RepoDir(req.Name)
	if err != nil {
		// Stack doesn't exist yet — that's expected.
		// Derive repoDir manually before the stack is registered.
		writeError(w, http.StatusInternalServerError, "could not determine repo dir: "+err.Error())
		return
	}

	// Clone.
	defer h.lockStack(req.Name)()
	ctx := r.Context()
	if err := h.git.Clone(ctx, req.Git.URL, req.Git.Branch, repoDir); err != nil {
		writeError(w, http.StatusBadGateway, "git clone: "+err.Error())
		return
	}

	stack, err := h.store.CreateFromGit(req.Name, &req.Git, req.Entrypoint)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, stack)
}

// gitInfo returns the git source configuration and sync status.
func (h *stackHandler) gitInfo(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if stack.Git == nil {
		writeError(w, http.StatusBadRequest, "stack is not git-backed")
		return
	}
	writeJSON(w, http.StatusOK, stack.Git)
}

// gitPull fetches and merges from origin.
func (h *stackHandler) gitPull(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if stack.Git == nil {
		writeError(w, http.StatusBadRequest, "stack is not git-backed")
		return
	}
	repoDir, err := h.store.RepoDir(name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "repository directory unavailable")
		return
	}
	ctx, cancel := httpx.Detached(r.Context(), h.deployTimeout)
	defer cancel()
	changed, commit, err := gitpkg.PullAndRecord(ctx, h.git, h.store, name, repoDir)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": changed, "commit": commit})
}

const maxGitPushOptionsBytes = 64 * 1024

// gitPush commits local changes and pushes to origin. Streams output.
func (h *stackHandler) gitPush(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if stack.Git == nil {
		writeError(w, http.StatusBadRequest, "stack is not git-backed")
		return
	}
	if stack.Git.ReadOnly {
		writeError(w, http.StatusForbidden, "stack is read-only")
		return
	}
	message, ok := decodeStackActionOption[string](w, r, "message", maxGitPushOptionsBytes)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if message == "" {
		message = "chore: update stack via brine"
	}

	repoDir, err := h.store.RepoDir(name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "repository directory unavailable")
		return
	}
	ctx, cancel := httpx.Detached(r.Context(), h.deployTimeout)
	defer cancel()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	fw := newFlushWriter(w)
	if err := h.git.Push(ctx, repoDir, message, fw); err != nil {
		writeActionStatus(fw, "git push", err)
		return
	}
	writeActionStatus(fw, "git push", nil)
}

func writeActionStatus(w http.ResponseWriter, action string, err error) {
	if err != nil {
		fmt.Fprintf(w, "\n[brine:error] %s failed: %s\n", action, err)
		return
	}
	fmt.Fprintf(w, "\n[brine:done] %s complete\n", action)
}

func (h *stackHandler) engine() string {
	if h.runtimeEngine == "" {
		return "docker"
	}
	return h.runtimeEngine
}

func (h *stackHandler) ensureRuntimeEngine(w http.ResponseWriter, stack *store.Stack) bool {
	content, err := h.store.ReadPkl(stack.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to read stack configuration")
		return false
	}
	declared := pkl.DeclaredEngine(content)
	if declared == "" || declared == h.engine() {
		return true
	}
	writeError(w, http.StatusConflict, fmt.Sprintf("stack declares %s but Brine is running against %s; update config.engine before running lifecycle actions", declared, h.engine()))
	return false
}

// gitUpdateSource updates the git source configuration (branch, folder, auto_deploy, etc.).
func (h *stackHandler) gitUpdateSource(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if stack.Git == nil {
		writeError(w, http.StatusBadRequest, "stack is not git-backed")
		return
	}
	var patch struct {
		Branch       *string `json:"branch"`
		Folder       *string `json:"folder"`
		ReadOnly     *bool   `json:"read_only"`
		AutoDeploy   *bool   `json:"auto_deploy"`
		PollInterval *string `json:"poll_interval"`
	}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	src := *stack.Git // copy
	if patch.Branch != nil {
		src.Branch = *patch.Branch
	}
	if patch.Folder != nil {
		src.Folder = *patch.Folder
	}
	if patch.ReadOnly != nil {
		src.ReadOnly = *patch.ReadOnly
	}
	if patch.AutoDeploy != nil {
		src.AutoDeploy = *patch.AutoDeploy
	}
	if patch.PollInterval != nil {
		src.PollInterval = *patch.PollInterval
	}
	if err := h.store.UpdateGitSource(name, &src); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, &src)
}

// lockStack shares the same coordinator with Git sync and secret previews.
func (h *stackHandler) lockStack(name string) func() {
	if h.agent == nil || h.agent.Dill() == nil {
		return func() {}
	}
	return h.agent.Dill().LockStack(name)
}
