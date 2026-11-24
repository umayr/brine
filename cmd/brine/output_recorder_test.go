package main

import (
	"net/http/httptest"
	"time"
)

// In-memory test writes never block; advertise that capability explicitly.
type deadlineRecorder struct{ *httptest.ResponseRecorder }

func (w *deadlineRecorder) SetWriteDeadline(time.Time) error { return nil }
