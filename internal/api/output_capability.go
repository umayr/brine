package api

import "net/http"

// Mirror ResponseController's capability traversal without flushing headers
// before the handler has decided to execute and committed its response.
func supportsOutputFlush(w http.ResponseWriter) bool {
	for depth := 0; depth < 32; depth++ {
		switch v := w.(type) {
		case interface{ FlushError() error }:
			return true
		case http.Flusher:
			return true
		case interface{ Unwrap() http.ResponseWriter }:
			w = v.Unwrap()
		default:
			return false
		}
	}
	return false
}
