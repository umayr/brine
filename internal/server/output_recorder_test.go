package server

import (
	"net/http/httptest"
	"time"
)

// This recorder only writes in memory and cannot block.
type deadlineRecorder struct{ *httptest.ResponseRecorder }

func (w *deadlineRecorder) SetWriteDeadline(time.Time) error { return nil }
