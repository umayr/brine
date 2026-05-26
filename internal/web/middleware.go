package web

import (
	"io"
	"net/http"
)

// isHTMX reports whether the request was initiated by HTMX.
func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// stripMuxHeader returns an io.Reader that strips Docker's 8-byte stream
// multiplexing header from each frame, yielding raw log text.
func stripMuxHeader(r io.Reader) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		header := make([]byte, 8)
		for {
			if _, err := io.ReadFull(r, header); err != nil {
				pw.CloseWithError(err)
				return
			}
			size := int64(header[4])<<24 | int64(header[5])<<16 | int64(header[6])<<8 | int64(header[7])
			if _, err := io.CopyN(pw, r, size); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
	}()
	return pr
}
