package api

import (
	"encoding/json"
	"net/http"
)

func health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// flushWriter wraps a ResponseWriter and flushes after every Write so clients
// see streaming output immediately instead of waiting for the command to finish.
type flushWriter struct {
	http.ResponseWriter
	err error
}

func newFlushWriter(w http.ResponseWriter) *flushWriter {
	fw := &flushWriter{ResponseWriter: w}
	return fw
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.ResponseWriter.Write(p)
	if err != nil {
		fw.err = err
	}
	fw.flush()
	return n, err
}

func (fw *flushWriter) flush() {
	if err := http.NewResponseController(fw.ResponseWriter).Flush(); err != nil && err != http.ErrNotSupported {
		fw.err = err
	}
}
