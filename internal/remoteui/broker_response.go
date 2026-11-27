package remoteui

import (
	"bytes"
	"errors"
	"net/http"
)

// Buffer bounded HTML so a failed post-dispatch protection fence never emits a
// false terminal receipt before the admission guard finishes.
type guardResponse struct {
	header http.Header
	body   bytes.Buffer
	code   int
	failed bool
}

func (r *guardResponse) Header() http.Header { return r.header }
func (r *guardResponse) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
}
func (r *guardResponse) Write(b []byte) (int, error) {
	if r.body.Len()+len(b) > 8<<20 {
		r.failed = true
		return 0, errors.New("response unavailable")
	}
	if r.code == 0 {
		r.code = 200
	}
	return r.body.Write(b)
}
