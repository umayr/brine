package agentclient

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestJournalRemoveRejectsMissingBindingAndReplayID(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, tc := range []struct{ name, journal, request string }{
		{"missing", "", strings.Repeat("b", 32)},
		{"malformed", "not-a-journal", strings.Repeat("b", 32)},
		{"same request", strings.Repeat("c", 32), id},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A nil client proves validation rejects before any session or transport.
			var c *Client
			_, err := c.RemoveJournalDeployment(context.Background(), "epoch", "demo", "sha256:"+strings.Repeat("d", 64), tc.request, id, tc.journal)
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
