//go:build linux

package api

import (
	"errors"
	"net/http"
)

type disconnectedWriter struct {
	header http.Header
	status int
}

func (w *disconnectedWriter) Header() http.Header       { return w.header }
func (w *disconnectedWriter) WriteHeader(code int)      { w.status = code }
func (w *disconnectedWriter) Write([]byte) (int, error) { return 0, errors.New("synthetic disconnect") }
func (w *disconnectedWriter) Flush()                    {}

// Linux GHA only: exercises the actual store with synthetic invocations; it is
// not native application/source/provider qualification or a runtime test.
