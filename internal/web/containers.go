package web

import (
	"bufio"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/engine"
)

func (h *Handler) containerList(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "containers/index.html", map[string]interface{}{
		"Page": "containers",
	})
}

func (h *Handler) containerDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	c, err := h.agent.Engine().InspectContainer(r.Context(), id)
	if err != nil {
		http.Error(w, "Container not found", http.StatusNotFound)
		return
	}
	h.render(w, r, "containers/detail.html", map[string]interface{}{
		"Page":      "containers",
		"Container": c,
	})
}

// --- HTMX partials ---

func (h *Handler) containerRows(w http.ResponseWriter, r *http.Request) {
	containers, err := h.agent.Engine().ListContainers(r.Context())
	if err != nil {
		h.renderAlert(w, "error", err.Error())
		return
	}
	h.renderPartial(w, r, "container-rows", containers)
}

func (h *Handler) containerStart(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.agent.Engine().StartContainer(r.Context(), id); err != nil {
		h.renderAlert(w, "error", err.Error())
		return
	}
	h.renderContainerRow(w, r, id)
}

func (h *Handler) containerStop(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.agent.Engine().StopContainer(r.Context(), id); err != nil {
		h.renderAlert(w, "error", err.Error())
		return
	}
	h.renderContainerRow(w, r, id)
}

func (h *Handler) containerRestart(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.agent.Engine().RestartContainer(r.Context(), id); err != nil {
		h.renderAlert(w, "error", err.Error())
		return
	}
	h.renderContainerRow(w, r, id)
}

func (h *Handler) renderContainerRow(w http.ResponseWriter, r *http.Request, id string) {
	c, err := h.agent.Engine().InspectContainer(r.Context(), id)
	if err != nil {
		h.renderAlert(w, "error", err.Error())
		return
	}
	// Render a single row as a slice of one
	h.renderPartial(w, r, "container-rows", []engine.ContainerInfo{*c})
}

// containerLogsSSE streams container logs as Server-Sent Events.
func (h *Handler) containerLogsSSE(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	tail := 100
	if t := r.URL.Query().Get("tail"); t != "" {
		if n, err := strconv.Atoi(t); err == nil {
			tail = n
		}
	}

	rc, err := h.agent.Engine().ContainerLogs(r.Context(), id, true, tail)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, canFlush := w.(http.Flusher)

	scanner := bufio.NewScanner(stripMuxHeader(rc))
	for scanner.Scan() {
		line := scanner.Text()
		// Each SSE message is a <div> appended to the log container
		fmt.Fprintf(w, "data: <div class=\"log-line\">%s</div>\n\n", escapeHTML(line))
		if canFlush {
			flusher.Flush()
		}
	}
	fmt.Fprintf(w, "event: done\ndata: <div class=\"log-line text-base-content/40\">— stream ended —</div>\n\n")
	if canFlush {
		flusher.Flush()
	}
}

func escapeHTML(s string) string {
	r := ""
	for _, c := range s {
		switch c {
		case '<':
			r += "&lt;"
		case '>':
			r += "&gt;"
		case '&':
			r += "&amp;"
		default:
			r += string(c)
		}
	}
	return r
}
