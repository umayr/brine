package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed templates
var templateFS embed.FS

// templateSet holds all parsed templates.
type templateSet struct {
	layout   *template.Template
	partials map[string]*template.Template
	pages    map[string]*template.Template
	funcs    template.FuncMap
}

var funcMap = template.FuncMap{
	"formatBytes": formatBytes,
	"rawJSON":     rawJSON,
}

// rawJSON converts a json.RawMessage to a display string.
func rawJSON(v json.RawMessage) string {
	if len(v) == 0 {
		return ""
	}
	// If it's a plain string, unwrap quotes.
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return s
	}
	return string(v)
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// parseTemplates parses all templates from the embedded FS.
// In dev mode, this re-reads from disk on each request.
func parseTemplates() (*templateSet, error) {
	ts := &templateSet{
		partials: make(map[string]*template.Template),
		pages:    make(map[string]*template.Template),
		funcs:    funcMap,
	}

	// Read layout
	layoutBytes, err := fs.ReadFile(templateFS, "templates/layout.html")
	if err != nil {
		return nil, fmt.Errorf("read layout: %w", err)
	}

	// Read all partials
	partialGlob, _ := fs.Glob(templateFS, "templates/partials/*.html")
	partialSrcs := map[string]string{}
	for _, p := range partialGlob {
		data, err := fs.ReadFile(templateFS, p)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(strings.TrimPrefix(p, "templates/partials/"), ".html")
		partialSrcs[name] = string(data)
	}

	// Parse partials as standalone templates
	for name, src := range partialSrcs {
		t, err := template.New(name).Funcs(funcMap).Parse(src)
		if err != nil {
			return nil, fmt.Errorf("parse partial %s: %w", name, err)
		}
		ts.partials[name] = t
	}

	// Parse page templates (each includes layout + itself)
	pagePaths, _ := fs.Glob(templateFS, "templates/**/*.html")
	rootPages, _ := fs.Glob(templateFS, "templates/*.html")
	pagePaths = append(pagePaths, rootPages...)

	for _, p := range pagePaths {
		if strings.Contains(p, "partials/") || p == "templates/layout.html" {
			continue
		}
		data, err := fs.ReadFile(templateFS, p)
		if err != nil {
			return nil, err
		}
		// Each page template is parsed together with the layout so that
		// {{template "layout.html" .}} works.
		t, err := template.New("layout.html").Funcs(funcMap).Parse(string(layoutBytes))
		if err != nil {
			return nil, fmt.Errorf("parse layout for %s: %w", p, err)
		}
		if _, err := t.New(p).Parse(string(data)); err != nil {
			return nil, fmt.Errorf("parse page %s: %w", p, err)
		}
		key := strings.TrimPrefix(p, "templates/")
		ts.pages[key] = t
	}

	return ts, nil
}

// render writes a full-page template response.
func (h *Handler) render(w http.ResponseWriter, r *http.Request, page string, data any) {
	ts, err := h.templates()
	if err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	t, ok := ts.pages[page]
	if !ok {
		http.Error(w, "template not found: "+page, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, data); err != nil {
		// Response already started; log and return
		fmt.Fprintf(w, "\n<!-- template error: %s -->", err)
	}
}

// renderPartial writes an HTML fragment from the partials map.
func (h *Handler) renderPartial(w http.ResponseWriter, r *http.Request, name string, data any) {
	ts, err := h.templates()
	if err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	t, ok := ts.partials[name]
	if !ok {
		http.Error(w, "partial not found: "+name, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, data); err != nil {
		fmt.Fprintf(w, "<!-- partial error: %s -->", err)
	}
}
