package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/engine"
	gitpkg "github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/store"
)

type stackHandler struct {
	agent agent.Agent
	store store.StackStore
	git   *gitpkg.Runner
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
	Engine     string `json:"engine"`
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
	if req.Engine == "" {
		req.Engine = "docker"
	}
	stack, err := h.store.Create(req.Name, req.Engine, req.PklContent)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
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
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	fw := newFlushWriter(w)
	if err := h.agent.Dill().Up(r.Context(), stack.PklPath, fw); err != nil {
		fmt.Fprintf(fw, "\nerror: %s\n", err)
	}
}

func (h *stackHandler) down(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fw := newFlushWriter(w)
	if err := h.agent.Dill().Down(r.Context(), stack.PklPath, fw); err != nil {
		fmt.Fprintf(fw, "\nerror: %s\n", err)
	}
}

func (h *stackHandler) stop(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fw := newFlushWriter(w)
	if err := h.agent.Dill().Stop(r.Context(), stack.PklPath, fw); err != nil {
		fmt.Fprintf(fw, "\nerror: %s\n", err)
	}
}

func (h *stackHandler) start(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fw := newFlushWriter(w)
	if err := h.agent.Dill().Start(r.Context(), stack.PklPath, fw); err != nil {
		fmt.Fprintf(fw, "\nerror: %s\n", err)
	}
}

func (h *stackHandler) restart(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fw := newFlushWriter(w)
	if err := h.agent.Dill().Restart(r.Context(), stack.PklPath, fw); err != nil {
		fmt.Fprintf(fw, "\nerror: %s\n", err)
	}
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
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fw := newFlushWriter(w)
	if err := h.agent.Dill().Teardown(r.Context(), stack.PklPath, req.Force, fw); err != nil {
		fmt.Fprintf(fw, "\nerror: %s\n", err)
	}
}

func (h *stackHandler) plan(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
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
		Engine     string          `json:"engine"`
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
	if req.Engine == "" {
		req.Engine = "docker"
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

	stack, err := h.store.CreateFromGit(req.Name, req.Engine, &req.Git, req.Entrypoint)
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
	ctx := r.Context()
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
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	fw := newFlushWriter(w)
	if err := h.git.Push(r.Context(), repoDir, body.Message, fw); err != nil {
		fmt.Fprintf(fw, "\nerror: %s\n", err)
	}
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
