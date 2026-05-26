package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
)

type imageHandler struct {
	agent agent.Agent
}

func (h *imageHandler) list(w http.ResponseWriter, r *http.Request) {
	images, err := h.agent.Engine().ListImages(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, images)
}

type pullRequest struct {
	Ref string `json:"ref"`
}

// pull streams pull progress as SSE events.
func (h *imageHandler) pull(w http.ResponseWriter, r *http.Request) {
	var req pullRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Ref == "" {
		writeError(w, http.StatusBadRequest, "ref is required")
		return
	}

	rc, err := h.agent.Engine().PullImage(r.Context(), req.Ref)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, canFlush := w.(http.Flusher)

	scanner := bufio.NewScanner(rc)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "data: %s\n\n", line)
		if canFlush {
			flusher.Flush()
		}
	}
	fmt.Fprintf(w, "event: done\ndata: pull complete\n\n")
	if canFlush {
		flusher.Flush()
	}
}

func (h *imageHandler) remove(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	force := r.URL.Query().Get("force") == "true"
	if err := h.agent.Engine().RemoveImage(r.Context(), id, force); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
