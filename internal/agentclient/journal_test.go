package agentclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestDeploymentJournalStrictCurrentEvidence(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, tc := range []struct {
		body, header string
		code         int
		ok           bool
	}{{fmt.Sprintf(`{"journalId":%q}`, id), id, 200, true}, {fmt.Sprintf(`{"JournalID":%q}`, id), id, 200, false}, {fmt.Sprintf(`{"journalId":%q,"extra":true}`, id), id, 200, false}, {fmt.Sprintf(`{"journalId":%q}`, id), "", 200, false}, {fmt.Sprintf(`{"journalId":%q}`, id), strings.Repeat("b", 32), 200, false}, {`{}`, id, 403, false}} {
		calls := 0
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.Method != "GET" || r.URL.Path != "/v1/deploy/journal" || r.Header.Get("Authorization") != "Bearer deploy" || r.Body != http.NoBody {
				t.Fatal("journal route/credential/body")
			}
			w.Header().Set("Content-Type", "application/json")
			if tc.header != "" {
				w.Header().Set(journalHeader, tc.header)
			}
			w.WriteHeader(tc.code)
			fmt.Fprint(w, tc.body)
		})
		c.bearer = "monitor"
		c.deployBearer = "deploy"
		c.epochCtx = context.Background()
		got, e := c.DeploymentJournal(context.Background(), testEpoch)
		if (e == nil) != tc.ok || tc.ok && got != id || calls != 1 {
			t.Fatal(got, e, calls)
		}
	}
}
