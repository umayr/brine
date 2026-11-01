package web

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/pkl"
	"github.com/umayr/brine/internal/store"
)

func (h *Handler) stackList(w http.ResponseWriter, r *http.Request) {
	stacks, err := h.store.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.render(w, r, "stacks/index.html", &stackListData{
		pageData: pageData{Page: "stacks"},
		Stacks:   stacks,
	})
}

func (h *Handler) stackNew(w http.ResponseWriter, r *http.Request) {
	byCategory, _ := h.recipes.ListByCategory()
	h.render(w, r, "stacks/new.html", &stackNewData{
		pageData:           pageData{Page: "stacks"},
		DefaultPkl:         defaultStackPkl(h.engine),
		Source:             r.URL.Query().Get("source"), // "local" | "git" | "gitops"
		Snippets:           byCategory,
		SnippetCompletions: h.snippetCompletions(),
	})
}

func defaultStackPkl(engine string) string {
	if engine == "" {
		engine = "docker"
	}
	return fmt.Sprintf(`amends "package://pkg.ushd.ca/dill@0.4.0#/dill.pkl"

config {
  name = "my-app"
  engine = "%s"
  services {
    ["web"] {
      image = "nginx:alpine"
      ports = List("8080:80")
    }
  }
}`, engine)
}

func (h *Handler) stackDetail(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}

	files, _ := h.store.Files(name)
	activeFile := stack.Entrypoint
	if activeFile == "" {
		activeFile = "compose.pkl"
	}

	fileContents := make(map[string]string, len(files))
	for _, f := range files {
		content, _ := h.store.ReadFile(name, f)
		fileContents[f] = content
	}

	h.render(w, r, "stacks/detail.html", &stackDetailData{
		pageData:        pageData{Page: "stacks"},
		Stack:           stack,
		Files:           files,
		ActiveFile:      activeFile,
		FileContents:    fileContents,
		EngineAlignment: h.engineAlignmentFor(stack, fileContents),
	})
}

func (h *Handler) stackEditor(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}

	// Active file: from query param or entrypoint.
	activeFile := r.URL.Query().Get("file")
	if activeFile == "" {
		if stack.Entrypoint != "" {
			activeFile = stack.Entrypoint
		} else {
			activeFile = "compose.pkl"
		}
	}

	content, _ := h.store.ReadFile(name, activeFile)
	files, _ := h.store.Files(name)
	byCategory, _ := h.recipes.ListByCategory()
	entrypoint := stack.Entrypoint
	if entrypoint == "" {
		entrypoint = "compose.pkl"
	}
	fileContents := map[string]string{activeFile: content}
	if activeFile != entrypoint {
		if entrypointContent, err := h.store.ReadFile(name, entrypoint); err == nil {
			fileContents[entrypoint] = entrypointContent
		}
	}

	h.render(w, r, "stacks/editor.html", &stackEditorData{
		pageData:           pageData{Page: "stacks"},
		Stack:              stack,
		ActiveFile:         activeFile,
		PklContent:         content,
		Files:              files,
		Snippets:           byCategory,
		SnippetCompletions: h.snippetCompletions(),
		EngineAlignment:    h.engineAlignmentFor(stack, fileContents),
	})
}

func (h *Handler) snippetCompletions() []map[string]string {
	items, err := h.recipes.List()
	if err != nil {
		return nil
	}
	completions := make([]map[string]string, 0, len(items))
	for _, item := range items {
		full, err := h.recipes.Get(item.Slug)
		if err != nil {
			continue
		}
		completions = append(completions, map[string]string{
			"name":        full.Name,
			"category":    full.Category,
			"description": full.Description,
			"body":        full.Body,
		})
	}
	return completions
}

// --- HTMX partials ---

func (h *Handler) stackCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderAlert(w, "error", "Invalid form data")
		return
	}
	name := r.FormValue("name")

	// Multi-file form sends filename[] + filecontent[]; fall back to legacy pkl_content.
	filenames := r.Form["filename"]
	filecontents := r.Form["filecontent"]

	var entrypointContent string
	if len(filenames) > 0 && len(filecontents) > 0 {
		entrypointContent = filecontents[0]
	} else {
		entrypointContent = r.FormValue("pkl_content")
	}

	if name == "" || entrypointContent == "" {
		h.renderAlert(w, "error", "Name and config are required")
		return
	}

	entrypointContent = pkl.WithStackName(entrypointContent, name)

	if _, err := h.store.Create(name, entrypointContent); err != nil {
		h.renderAlert(w, "error", err.Error())
		return
	}

	// Write any additional files (index 0 is compose.pkl, already written by Create).
	for i := 1; i < len(filenames) && i < len(filecontents); i++ {
		if filenames[i] == "" {
			continue
		}
		if err := h.store.WriteFile(name, filenames[i], filecontents[i]); err != nil {
			h.renderAlert(w, "error", fmt.Sprintf("Stack created but failed to write %s: %s", filenames[i], err.Error()))
			return
		}
	}

	w.Header().Set("HX-Redirect", "/stacks/"+name+"/edit")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) stackUpdate(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := r.ParseForm(); err != nil {
		h.renderAlert(w, "error", "Invalid form data")
		return
	}
	pkl := r.FormValue("pkl_content")
	filename := r.FormValue("active_file")
	if filename != "" {
		if err := h.store.WriteFile(name, filename, pkl); err != nil {
			h.renderAlert(w, "error", err.Error())
			return
		}
	} else {
		if _, err := h.store.Update(name, pkl); err != nil {
			h.renderAlert(w, "error", err.Error())
			return
		}
	}
	w.Header().Set("HX-Redirect", "/stacks/"+name)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) stackValidate(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		h.renderAlert(w, "error", "Stack not found")
		return
	}
	_ = r.ParseForm()
	content := r.FormValue("pkl_content")
	if content != "" {
		// Validate in-memory content (e.g. from the editor before saving).
		if err := h.agent.Dill().ValidateContent(r.Context(), stack.PklPath, content); err != nil {
			h.renderAlert(w, "error", err.Error())
			return
		}
	} else {
		// Validate the file on disk.
		if err := h.agent.Dill().Validate(r.Context(), stack.PklPath); err != nil {
			h.renderAlert(w, "error", err.Error())
			return
		}
	}
	h.renderAlert(w, "success", "Valid ✓")
}

func (h *Handler) stackFmt(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		h.renderAlert(w, "error", "Stack not found")
		return
	}
	_ = r.ParseForm()
	activeFile := r.FormValue("active_file")
	content := r.FormValue("pkl_content")

	// Derive the path for context (PKL import resolution); content comes from the form.
	formatPath := stack.PklPath
	if activeFile != "" {
		formatPath = filepath.Join(filepath.Dir(stack.PklPath), activeFile)
	}

	formatted, err := h.agent.Dill().Fmt(r.Context(), formatPath, content)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprintf(w, `<div class="alert alert-error mt-3"><pre class="whitespace-pre-wrap font-mono text-sm break-words">%s</pre></div>`, htmlEscape(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, formatted)
}

func (h *Handler) stackPlan(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		h.renderAlert(w, "error", "Stack not found")
		return
	}
	content, _ := h.store.ReadPkl(name)
	entrypoint := stack.Entrypoint
	if entrypoint == "" {
		entrypoint = "compose.pkl"
	}
	alignment := h.engineAlignmentFor(stack, map[string]string{entrypoint: content})
	if alignment.Blocked {
		h.renderAlert(w, "warning", alignment.Warning+" Update config.engine before planning or deploying.")
		return
	}
	result, err := h.agent.Dill().Plan(r.Context(), stack.PklPath)
	if err != nil {
		h.renderAlert(w, "error", err.Error())
		return
	}
	h.renderPartial(w, r, "plan-diff", result)
}

func (h *Handler) stackContainers(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil {
		h.renderPartial(w, r, "stack-containers", nil)
		return
	}
	psContainers, err := h.agent.Dill().PS(r.Context(), stack.PklPath)
	if err != nil {
		// Stack may not be deployed yet — show empty state, not an error.
		h.renderPartial(w, r, "stack-containers", nil)
		return
	}
	containers := make([]engine.ContainerInfo, 0, len(psContainers))
	for _, c := range psContainers {
		containers = append(containers, engine.ContainerInfo{
			ID:     c.ID,
			Name:   c.Name,
			State:  c.State,
			Status: c.Status,
			Image:  c.Image,
			Ports:  c.Ports,
			Stack:  name,
		})
	}
	h.renderPartial(w, r, "stack-containers", containers)
}

// --- Stack file HTMX partials ---

func (h *Handler) stackFileCreate(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := r.ParseForm(); err != nil {
		h.renderAlert(w, "error", "Invalid form data")
		return
	}
	filename := r.FormValue("filename")
	if filename == "" {
		h.renderAlert(w, "error", "Filename is required")
		return
	}
	if err := h.store.WriteFile(name, filename, ""); err != nil {
		h.renderAlert(w, "error", err.Error())
		return
	}
	// Redirect to editor with the new file active.
	w.Header().Set("HX-Redirect", "/stacks/"+name+"/edit?file="+filename)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) stackFileUpdate(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	filename := chi.URLParam(r, "filename")
	if err := r.ParseForm(); err != nil {
		h.renderAlert(w, "error", "Invalid form data")
		return
	}
	content := r.FormValue("pkl_content")
	if err := h.store.WriteFile(name, filename, content); err != nil {
		h.renderAlert(w, "error", err.Error())
		return
	}
	h.renderAlert(w, "success", "Saved")
}

func (h *Handler) stackFileDelete(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	filename := chi.URLParam(r, "filename")
	if err := h.store.DeleteFile(name, filename); err != nil {
		h.renderAlert(w, "error", err.Error())
		return
	}
	w.Header().Set("HX-Redirect", "/stacks/"+name+"/edit")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) stackSetEntrypoint(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := r.ParseForm(); err != nil {
		h.renderAlert(w, "error", "Invalid form data")
		return
	}
	filename := r.FormValue("filename")
	if err := h.store.SetEntrypoint(name, filename); err != nil {
		h.renderAlert(w, "error", err.Error())
		return
	}
	h.renderAlert(w, "success", "Entrypoint set to "+filename)
}

// --- Git HTMX partials ---

// stackGitCreate clones a remote repo and registers the stack.
// Accepts a form with: name, engine, git_url, git_branch, git_folder,
// git_auto_deploy (checkbox), git_poll_interval, git_entrypoint.
func (h *Handler) stackGitCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderAlert(w, "error", "Invalid form data")
		return
	}
	name := r.FormValue("name")
	gitURL := r.FormValue("git_url")
	branch := r.FormValue("git_branch")
	folder := r.FormValue("git_folder")
	entrypoint := r.FormValue("git_entrypoint")
	autoDeploy := r.FormValue("git_auto_deploy") == "on"
	readOnly := r.FormValue("git_read_only") == "on"
	pollInterval := r.FormValue("git_poll_interval")

	if name == "" || gitURL == "" {
		h.renderAlert(w, "error", "Name and Git URL are required")
		return
	}
	if branch == "" {
		branch = "main"
	}

	src := &store.GitSource{
		URL:          gitURL,
		Branch:       branch,
		Folder:       folder,
		ReadOnly:     readOnly,
		AutoDeploy:   autoDeploy,
		PollInterval: pollInterval,
	}

	repoDir, _ := h.store.RepoDir(name)
	if err := h.git.Clone(r.Context(), gitURL, branch, repoDir); err != nil {
		h.renderAlert(w, "error", "git clone failed: "+err.Error())
		return
	}
	if _, err := h.store.CreateFromGit(name, src, entrypoint); err != nil {
		h.renderAlert(w, "error", err.Error())
		return
	}
	w.Header().Set("HX-Redirect", "/stacks/"+name)
	w.WriteHeader(http.StatusOK)
}

// stackGitPull pulls from origin and shows the result.
func (h *Handler) stackGitPull(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	stack, err := h.store.Get(name)
	if err != nil || stack.Git == nil {
		h.renderAlert(w, "error", "Stack not found or not git-backed")
		return
	}
	repoDir, _ := h.store.RepoDir(name)
	ctx, cancel := httpx.Detached(r.Context(), h.deployTimeout)
	defer cancel()
	changed, err := h.git.Pull(ctx, repoDir)
	if err != nil {
		h.renderAlert(w, "error", "git pull: "+err.Error())
		return
	}
	commit, _ := h.git.ShortRef(ctx, repoDir)
	_ = h.store.UpdateGitStatus(name, commit, "", timeNow())
	if changed {
		h.renderAlert(w, "success", fmt.Sprintf("Pulled — now at %s", commit))
	} else {
		h.renderAlert(w, "info", fmt.Sprintf("Already up to date (%s)", commit))
	}
}

// stackGitUpdateSource saves updated git source settings.
func (h *Handler) stackGitUpdateSource(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := r.ParseForm(); err != nil {
		h.renderAlert(w, "error", "Invalid form data")
		return
	}
	stack, err := h.store.Get(name)
	if err != nil || stack.Git == nil {
		h.renderAlert(w, "error", "Stack not found or not git-backed")
		return
	}
	src := *stack.Git
	if v := r.FormValue("git_branch"); v != "" {
		src.Branch = v
	}
	src.Folder = r.FormValue("git_folder")
	src.ReadOnly = r.FormValue("git_read_only") == "on"
	src.AutoDeploy = r.FormValue("git_auto_deploy") == "on"
	src.PollInterval = r.FormValue("git_poll_interval")

	if err := h.store.UpdateGitSource(name, &src); err != nil {
		h.renderAlert(w, "error", err.Error())
		return
	}
	h.renderAlert(w, "success", "Git settings saved")
}

func timeNow() time.Time { return time.Now() }

// renderAlert writes a DaisyUI alert fragment for HTMX swap targets.
func (h *Handler) renderAlert(w http.ResponseWriter, alertType, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div class="alert alert-%s mt-3"><pre class="whitespace-pre-wrap font-mono text-sm break-words">%s</pre></div>`,
		alertType, htmlEscape(msg))
}

func htmlEscape(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&#34;",
	)
	return replacer.Replace(s)
}
