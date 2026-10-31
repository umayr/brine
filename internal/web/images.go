package web

import (
	"bufio"
	"fmt"
	"net/http"
)

func (h *Handler) imageList(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "images/index.html", &imageListData{
		pageData: pageData{Page: "images"},
	})
}

// --- HTMX partials ---

func (h *Handler) imageRows(w http.ResponseWriter, r *http.Request) {
	images, err := h.agent.Engine().ListImages(r.Context())
	if err != nil {
		h.renderAlert(w, "error", err.Error())
		return
	}
	h.renderPartial(w, r, "image-rows", images)
}

// imagePull streams pull output as SSE.
func (h *Handler) imagePull(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderAlert(w, "error", "Invalid form")
		return
	}
	ref := r.FormValue("ref")
	if ref == "" {
		h.renderAlert(w, "error", "Image reference is required")
		return
	}

	rc, err := h.agent.Engine().PullImage(r.Context(), ref)
	if err != nil {
		h.renderAlert(w, "error", err.Error())
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, canFlush := w.(http.Flusher)
	scanner := bufio.NewScanner(rc)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "data: <div class=\"log-line\">%s</div>\n\n", escapeHTML(line))
		if canFlush {
			flusher.Flush()
		}
	}
	fmt.Fprintf(w, "event: done\ndata: <div class=\"log-line text-success\">— pull complete —</div>\n\n")
	if canFlush {
		flusher.Flush()
	}
}
