package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type cleanupRoundTripper func(*http.Request) (*http.Response, error)

func (f cleanupRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestUncertainIncomingCleanupPinsObservedReplacementOnly(t *testing.T) {
	old, newID, netID, foreign := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)
	client := &http.Client{Transport: cleanupRoundTripper(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch r.URL.Path {
		case "/containers/json":
			body = `[{"Id":"` + newID + `","Labels":{"dill.stack":"fixture","dill.managed":"true"}},{"Id":"` + foreign + `","Labels":{"dill.stack":"other","dill.managed":"true"}}]`
		case "/containers/" + newID + "/json":
			body = `{"Id":"` + newID + `","NetworkSettings":{"Networks":{"fixture":{"NetworkID":"` + netID + `"}}}}`
		case "/networks/" + netID:
			body = `{"Id":"` + netID + `","Name":"fixture"}`
		default:
			t.Fatal("unexpected cleanup observation", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	ids := []string{old}
	network := ""
	nets := collectIncomingCleanup(t, "fixture", client, &ids, &network)
	if len(ids) != 2 || ids[1] != newID || len(nets) != 1 || nets[0] != netID {
		t.Fatal("uncertain replacement not pinned, or unrelated object acquired", ids, nets)
	}
}
