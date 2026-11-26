package agentclient

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestLifecycleUsesDeploymentCustodyAndPreservesNoop(t *testing.T) {
	for _, action := range []string{"start", "stop", "restart"} {
		t.Run(action, func(t *testing.T) {
			journal, id, prior := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
			calls := 0
			replyAction := action
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/v1/deploy/"+action || r.Method != "POST" || r.Header.Get(journalHeader) != journal || r.Header.Get("Authorization") != "Bearer deploy" {
					t.Fatal("lifecycle crossed credential/journal/route boundary")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set(journalHeader, journal)
				json.NewEncoder(w).Encode(DeploymentStatus{Action: replyAction, RequestID: id, Stack: "demo", State: "succeeded", Effects: "none", OperationID: "op_" + id})
			})
			c.bearer = "monitor"
			c.deployBearer = "deploy"
			c.epochCtx = context.Background()
			submit := func() (DeploymentStatus, error) {
				return c.ApplyLifecycle(context.Background(), testEpoch, "demo", action, "sha256:"+strings.Repeat("d", 64), id, prior, journal)
			}
			got, e := submit()
			if e != nil || got.Action != action || got.Effects != "none" || got.State != "succeeded" || calls != 1 {
				t.Fatal(got, e, calls)
			}
			replyAction = ""
			if _, e = submit(); e == nil || calls != 2 {
				t.Fatal("accepted unbound lifecycle result or retried")
			}
			replyAction = "down"
			if _, e = submit(); e == nil || calls != 3 {
				t.Fatal("accepted unknown lifecycle action or retried")
			}
		})
	}
}
