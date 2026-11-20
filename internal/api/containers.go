package api

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/docker/docker/errdefs"
	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/target"
)

type containerHandler struct {
	agent agent.Agent
}

func (h *containerHandler) list(w http.ResponseWriter, r *http.Request) {
	containers, err := h.agent.Engine().ListContainers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Container inventory unavailable; check the local container engine and retry")
		return
	}
	writeJSON(w, http.StatusOK, containers)
}

func (h *containerHandler) get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	c, err := h.agent.Engine().InspectContainer(r.Context(), id)
	if err != nil {
		if errdefs.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "Container not found")
		} else {
			writeError(w, http.StatusBadGateway, "Container inspection unavailable; check the local container engine and retry")
		}
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *containerHandler) start(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.agent.Engine().StartContainer(r.Context(), id); err != nil {
		msg := "Container start failed; check the container state before retrying"
		switch {
		case errors.Is(err, engine.ErrCheckedUpRequired):
			msg = engine.ErrCheckedUpRequired.Error()
		case errors.Is(err, target.ErrNegotiationUnavailable):
			msg = target.ErrNegotiationUnavailable.Error()
		}
		writeError(w, http.StatusInternalServerError, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

func (h *containerHandler) stop(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.agent.Engine().StopContainer(r.Context(), id); err != nil {
		msg := "Container stop failed; check the container state before retrying"
		if errors.Is(err, target.ErrNegotiationUnavailable) {
			msg = target.ErrNegotiationUnavailable.Error()
		}
		writeError(w, http.StatusInternalServerError, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (h *containerHandler) restart(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.agent.Engine().RestartContainer(r.Context(), id); err != nil {
		msg := "Container restart failed; check the container state before retrying"
		if errors.Is(err, engine.ErrCheckedUpRequired) {
			msg = engine.ErrCheckedUpRequired.Error()
		}
		writeError(w, http.StatusInternalServerError, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarted"})
}

// logs streams container logs as Server-Sent Events.
// Each log line is emitted as: data: <line>\n\n
func (h *containerHandler) logs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	follow := r.URL.Query().Get("follow") == "true"
	tail := 100
	if t := r.URL.Query().Get("tail"); t != "" {
		if n, err := strconv.Atoi(t); err == nil {
			tail = n
		}
	}

	rc, err := h.agent.Engine().ContainerLogs(r.Context(), id, follow, tail)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Container logs unavailable; check the container and local engine, then retry")
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, canFlush := w.(http.Flusher)

	// The engine adapter has already demultiplexed stdout/stderr.
	scanner := bufio.NewScanner(rc)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "data: %s\n\n", line)
		if canFlush {
			flusher.Flush()
		}
	}
	terminal := "stream ended"
	if scanner.Err() != nil {
		terminal = "Log stream incomplete; check the local container engine and retry"
	}
	fmt.Fprintf(w, "event: done\ndata: %s\n\n", terminal)
	if canFlush {
		flusher.Flush()
	}
}
