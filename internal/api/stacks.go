package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/engine"
	gitpkg "github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/pkl"
	"github.com/umayr/brine/internal/store"
)

type stackHandler struct {
	agent         agent.Agent
	store         store.StackStore
	git           *gitpkg.Runner
	runtimeEngine string
	deployTimeout time.Duration
}

type stackResponse struct {
	*store.Stack
	PklContent string `json:"pkl_content,omitempty"`
}

func (h *stackHandler) list(w http.ResponseWriter, r *http.Request) {
	stacks, err := h.store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stacks)
}

func (h *stackHandler) get(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	pkl, _ := h.store.ReadPkl(name)
	writeJSON(w, http.StatusOK, stackResponse{Stack: stack, PklContent: pkl})
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
	var req updateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	stack, err := h.store.Update(name, req.PklContent)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stack)
}

func (h *stackHandler) delete(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var req struct {
		RemoveContainers bool `json:"remove_containers"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	if req.RemoveContainers {
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

func (h *stackHandler) up(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if !h.ensureRuntimeEngine(w, stack) {
		return
	}
	ctx, cancel := httpx.Detached(r.Context(), h.deployTimeout)
	defer cancel()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	fw := newFlushWriter(w)
	writeActionStatus(fw, "up", h.agent.Dill().Up(ctx, stack.PklPath, fw))
}

func (h *stackHandler) down(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
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
	writeActionStatus(fw, "down", h.agent.Dill().Down(ctx, stack.PklPath, fw))
}

func (h *stackHandler) stop(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
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
	writeActionStatus(fw, "stop", h.agent.Dill().Stop(ctx, stack.PklPath, fw))
}

func (h *stackHandler) start(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
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
	writeActionStatus(fw, "start", h.agent.Dill().Start(ctx, stack.PklPath, fw))
}

func (h *stackHandler) restart(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
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
	writeActionStatus(fw, "restart", h.agent.Dill().Restart(ctx, stack.PklPath, fw))
}

type teardownRequest struct {
	Force bool `json:"force"`
}

func (h *stackHandler) teardown(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var req teardownRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
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
	writeActionStatus(fw, "teardown", h.agent.Dill().Teardown(ctx, stack.PklPath, req.Force, fw))
}

func (h *stackHandler) plan(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
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
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
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
	all, err := h.agent.Engine().ListContainers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var filtered []engine.ContainerInfo
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
	files, err := h.store.Files(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (h *stackHandler) getFile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	filename := chi.URLParam(r, "filename")
	content, err := h.store.ReadFile(name, filename)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"filename": filename, "content": content})
}

func (h *stackHandler) writeFile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
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
	filename := chi.URLParam(r, "filename")
	if err := h.store.DeleteFile(name, filename); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *stackHandler) setEntrypoint(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
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
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if stack.Git == nil {
		writeError(w, http.StatusBadRequest, "stack is not git-backed")
		return
	}
	repoDir, _ := h.store.RepoDir(name)
	ctx, cancel := httpx.Detached(r.Context(), h.deployTimeout)
	defer cancel()
	changed, err := h.git.Pull(ctx, repoDir)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	commit, _ := h.git.ShortRef(ctx, repoDir)
	_ = h.store.UpdateGitStatus(name, commit, "", time.Now())
	writeJSON(w, http.StatusOK, map[string]any{"changed": changed, "commit": commit})
}

// gitPush commits local changes and pushes to origin. Streams output.
func (h *stackHandler) gitPush(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
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
	var body struct {
		Message string `json:"message"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Message == "" {
		body.Message = "chore: update stack via brine"
	}

	repoDir, _ := h.store.RepoDir(name)
	ctx, cancel := httpx.Detached(r.Context(), h.deployTimeout)
	defer cancel()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	fw := newFlushWriter(w)
	if err := h.git.Push(ctx, repoDir, body.Message, fw); err != nil {
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
		writeError(w, http.StatusInternalServerError, err.Error())
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
