package api

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
)

type containerHandler struct {
	agent agent.Agent
}

func (h *containerHandler) list(w http.ResponseWriter, r *http.Request) {
	containers, err := h.agent.Engine().ListContainers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, containers)
}

func (h *containerHandler) get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	c, err := h.agent.Engine().InspectContainer(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *containerHandler) start(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.agent.Engine().StartContainer(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

func (h *containerHandler) stop(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.agent.Engine().StopContainer(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (h *containerHandler) restart(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.agent.Engine().RestartContainer(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
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

	// Docker multiplexes stdout/stderr with an 8-byte header per frame.
	// Use a simple line scanner to emit lines as SSE events.
	scanner := bufio.NewScanner(stripDockerHeader(rc))
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "data: %s\n\n", line)
		if canFlush {
			flusher.Flush()
		}
	}
	fmt.Fprintf(w, "event: done\ndata: stream ended\n\n")
	if canFlush {
		flusher.Flush()
	}
}

// stripDockerHeader returns an io.Reader that strips the 8-byte Docker stream
// multiplexing header from each log frame, yielding clean log text.
func stripDockerHeader(r io.Reader) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		header := make([]byte, 8)
		for {
			_, err := io.ReadFull(r, header)
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			// header[4:8] is the big-endian frame size.
			size := int64(header[4])<<24 | int64(header[5])<<16 | int64(header[6])<<8 | int64(header[7])
			if _, err := io.CopyN(pw, r, size); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
	}()
	return pr
}
