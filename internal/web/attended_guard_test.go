package web

import (
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/httpx"
)

func TestDisabledAttendedMount(t *testing.T) {
	if err := MountAttended(chi.NewRouter(), nil, httpx.Timeouts{}, nil); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("attended web mount: %v", err)
	}
}
