package web

import (
	"html/template"
	"net/http"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/projectsnapshot"
)

// This preview is deliberately bounded and discards captured content immediately.
var previewLimits = projectsnapshot.Limits{MaxFiles: 1000, MaxEntries: 2000, MaxDepth: 32, MaxFileBytes: 4 << 20, MaxTotalBytes: 16 << 20}

type previewFile struct {
	Path  string
	Bytes int
}
type previewData struct {
	Entrypoint  string
	Files       []previewFile
	Total       int
	Unsupported bool
}

var projectPreviewTemplate = template.Must(template.New("preview").Parse(`
<div role="status" class="space-y-2">
  <p>Read-only preview: {{len .Files}} regular files, {{.Total}} bytes total. Entrypoint: <code>{{.Entrypoint}}</code>.</p>
  {{if .Unsupported}}<p role="alert">Transfer unsupported: this project is incompatible with the provisional Dill snapshot receiver. Rename or adjust incompatible files before any future review or transfer.</p>{{end}}
  <p>Dill staging compatibility is a versioned provisional contract, including the encoded upload size limit, and must be revalidated by the server. This preview does not authorize or transfer anything.</p>
  <ul aria-label="Canonical regular files">{{range .Files}}<li><code>{{.Path}}</code> — {{.Bytes}} bytes</li>{{end}}</ul>
</div>`))

func (h *Handler) projectPreview(w http.ResponseWriter, r *http.Request) {
	if !h.qualifiedReadAllowed(w, r) {
		return
	}
	name := chi.URLParam(r, "name")
	defer h.lockStack(name)()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	stack, err := h.store.Get(name)
	if err != nil || stack == nil {
		http.Error(w, "Project preview unavailable", http.StatusNotFound)
		return
	}
	// Build opens the final root without following symlinks, but cannot anchor
	// intermediate Git folder components. Fail closed until it supports that.
	if stack.Git != nil && stack.Git.Folder != "" {
		http.Error(w, "Project preview unavailable for Git subfolders", http.StatusUnprocessableEntity)
		return
	}
	entry := stack.Entrypoint
	if entry == "" {
		entry = "compose.pkl"
	}
	// PklPath is recomputed and validated by the store; do not accept paths from the request.
	snap, err := projectsnapshot.Build(filepath.Dir(stack.PklPath), entry, previewLimits)
	if err != nil {
		http.Error(w, "Project preview unavailable. Check project files, file types and size limits, then retry.", http.StatusUnprocessableEntity)
		return
	}
	data := previewData{Entrypoint: snap.Entrypoint, Unsupported: projectsnapshot.ValidateProvisionalReceiver(r.Context(), snap) != nil}
	for _, f := range snap.Files {
		data.Files = append(data.Files, previewFile{f.Path, len(f.Content)})
		data.Total += len(f.Content)
	}
	_ = projectPreviewTemplate.Execute(w, data)
}
