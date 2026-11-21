package web

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"
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
		h.renderPartial(w, r, "image-list-error", err.Error())
		return
	}
	h.renderPartial(w, r, "image-rows", images)
}

// imagePull sends one notice to the HTMX form after reading the engine response.
// The separate API endpoint retains its existing streaming response.
func (h *Handler) imagePull(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderAlert(w, "error", "Invalid image pull form. Check the reference and try again.")
		return
	}
	ref := strings.TrimSpace(r.FormValue("ref"))
	if ref == "" {
		h.renderAlert(w, "error", "Image reference is required.")
		return
	}

	rc, err := h.agent.Engine().PullImage(r.Context(), ref)
	if err != nil {
		if r.Context().Err() != nil {
			h.renderAlert(w, "warning", imagePullUnconfirmed)
			return
		}
		h.renderAlert(w, "error", "Could not start reading image pull progress. Check the image reference and local engine; inspect image inventory before retrying.")
		return
	}
	defer rc.Close()

	reportedError, completeProgress := readImagePullProgress(rc)
	switch {
	case reportedError:
		h.renderAlert(w, "error", "The engine reported an image pull error. Check the image reference and local engine before retrying.")
	case completeProgress && r.Context().Err() == nil:
		h.renderAlert(w, "info", "Engine finished reporting pull progress without an error. Refresh image inventory to verify the image.")
	default:
		h.renderAlert(w, "warning", imagePullUnconfirmed)
	}
}

const imagePullUnconfirmed = "Image pull could not be confirmed. Refresh image inventory and check the local engine before retrying."

// Docker-compatible pulls report progress as newline-delimited JSON records.
// Unknown records make completion uncertain; later reported errors still count.
// Clean progress EOF is not evidence that the image is in local inventory.
func readImagePullProgress(r io.Reader) (reportedError, completeProgress bool) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var seenProgress, unrecognized bool
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record struct {
			Status      string
			Error       string
			ErrorDetail struct{ Message string }
		}
		if json.Unmarshal([]byte(line), &record) != nil {
			unrecognized = true
			continue
		}
		if record.Error != "" || record.ErrorDetail.Message != "" {
			return true, false
		}
		if record.Status != "" {
			seenProgress = true
		} else {
			unrecognized = true
		}
	}
	return false, seenProgress && !unrecognized && scanner.Err() == nil
}
