package agentclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestLifecycleFenceReturnsExistingActionWithoutReplaying(t *testing.T) {
	id, journal := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, action := range []string{"start", "stop", "restart"} {
		calls := 0
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.URL.Path != "/v1/deploy/fence" || r.Header.Get(journalHeader) != journal {
				t.Fatal("not fence")
			}
			w.Header().Set(journalHeader, journal)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"requestId":%q,"stack":"demo","state":"succeeded","effects":"none","operationId":%q,"action":%q}`, id, "op_"+id, action)
		})
		c.bearer = "monitor"
		c.deployBearer = "deployer"
		c.epochCtx = context.Background()
		got, e := c.FenceDeployment(context.Background(), testEpoch, id, "demo", journal)
		if e != nil || got.Action != action || got.Effects != "none" || calls != 1 {
			t.Fatal(got, e, calls)
		}
	}
}
func TestLifecycleActionKeyMustBeValidWhenPresent(t *testing.T) {
	base := `{"requestId":"id","stack":"demo","state":"unknown","effects":"unknown"`
	for _, value := range []string{`""`, `null`, `"down"`, `42`} {
		if canonicalDeploymentStatusKeys([]byte(base + `,"action":` + value + `}`)) {
			t.Fatal("malformed action accepted", value)
		}
	}
	if !canonicalDeploymentStatusKeys([]byte(base+`}`)) || !canonicalDeploymentStatusKeys([]byte(base+`,"action":"stop"}`)) {
		t.Fatal("valid legacy/action rejected")
	}
}
