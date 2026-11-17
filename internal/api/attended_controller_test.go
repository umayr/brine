package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAttendedFieldsClosedAndBounded(t *testing.T) {
	for _, raw := range []string{`{}`, `{"action":null}`, `{"action":2}`, `{"action":"up","action":"up"}`, `{"action":"up","extra":false}`, `{"action":"up"}{}`, `{"action":"up"}` + strings.Repeat(" ", 4097)} {
		req := httptest.NewRequest("POST", "/", strings.NewReader(raw))
		if _, err := attendedFields(req, "action"); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"action":"up"}`))
	got, err := attendedFields(req, "action")
	if err != nil || got["action"] != "up" {
		t.Fatal(got, err)
	}
}
