package agentclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestCheckedApplyDoesNotFallBackOrSwapPrior(t *testing.T) {
	id, prior, journal := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	for _, code := range []int{200, 403, 404, 409} {
		calls := 0
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.URL.Path != "/v1/deploy/checked-apply" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer deploy" || r.Header.Get(journalHeader) != journal {
				t.Fatal("checked route/credential boundary changed")
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set(journalHeader, journal)
			w.WriteHeader(code)
			fmt.Fprintf(w, `{"requestId":%q,"stack":"demo","state":"succeeded","effects":"complete","operationId":%q}`, id, "op_"+id)
		})
		c.bearer = "monitor"
		c.deployBearer = "deploy"
		c.epochCtx = context.Background()
		got, e := c.ApplyCheckedJournalDeployment(context.Background(), testEpoch, strings.Repeat("d", 32), strings.Repeat("e", 64), "demo", "sha256:"+strings.Repeat("f", 64), id, prior, journal)
		if (e == nil) != (code == 200) || calls != 1 || code == 200 && got.State != "succeeded" {
			t.Fatal(got, e, calls)
		}
	}
}
