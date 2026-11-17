package agentclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestOversizeResponse(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, strings.Repeat(" ", 512*1024+1))
	})
	if _, e := c.Inventory(context.Background(), testEpoch, nil, 1); e == nil {
		t.Fatal("oversize accepted")
	}
}
func TestInspectRejectsIncompleteAndSecretFields(t *testing.T) {
	for _, data := range []string{`{"containerId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","stack":"s","service":"v","observedAt":"2025-01-01T00:00:00Z","inspect":{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`, `{"containerId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","stack":"s","service":"v","observedAt":"2025-01-01T00:00:00Z","inspect":{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","env":["SECRET=value"]}}`} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) { reply(w, r, data) })
		if _, e := c.Inspect(context.Background(), testEpoch, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); e == nil {
			t.Fatal("invalid inspect accepted")
		}
	}
}
func TestLogOversizeAndSequence(t *testing.T) {
	for _, chunk := range []string{`{"type":"heartbeat","sequence":2}`, `{"type":"chunk","sequence":1,"stream":"stdout","bytesBase64":"` + strings.Repeat("A", 24000) + `"}`} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/x-ndjson")
			fmt.Fprintf(w, "{\"version\":%q,\"requestId\":%q,\"data\":%s}\n", version, r.Header.Get("X-Request-Id"), chunk)
		})
		if e := c.Logs(context.Background(), testEpoch, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", LogOptions{Tail: 1}, func(context.Context, LogEvent) error { return nil }); e == nil {
			t.Fatal("invalid stream accepted")
		}
	}
}
