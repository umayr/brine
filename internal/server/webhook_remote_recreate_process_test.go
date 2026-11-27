package server

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"testing"
)

// Called only after an actually verified successful target deployment. Preserve
// both pinned IDs until absence is proven, so failures retain bounded cleanup.
func pinIncomingReplacement(t *testing.T, stack, command, network string, ids *[]string, engine *http.Client, call func(string, string) []byte) {
	t.Helper()
	old := (*ids)[0]
	var rows []struct {
		ID     string `json:"Id"`
		Labels map[string]string
	}
	if json.Unmarshal(call("GET", "/containers/json?all=1"), &rows) != nil {
		t.Fatal("replacement inventory")
	}
	found := []string{}
	for _, r := range rows {
		if r.Labels["dill.stack"] == stack && r.Labels["dill.managed"] == "true" {
			if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(r.ID) {
				t.Fatal("replacement ID invalid")
			}
			if r.ID != old {
				*ids = append(*ids, r.ID)
			}
			found = append(found, r.ID)
		}
	}
	if len(found) != 1 || found[0] == old {
		t.Fatal("deployment did not replace sole managed container", found)
	}
	next := found[0]
	var inspection struct {
		ID              string `json:"Id"`
		State           struct{ Running bool }
		Config          struct{ Cmd []string }
		NetworkSettings struct {
			Networks map[string]struct{ NetworkID string }
		}
	}
	if json.Unmarshal(call("GET", "/containers/"+next+"/json"), &inspection) != nil || inspection.ID != next || !inspection.State.Running || !reflect.DeepEqual(inspection.Config.Cmd, []string{"sh", "-c", "trap 'exit 0' TERM; while true; do " + command + "; done"}) || inspection.NetworkSettings.Networks[stack].NetworkID != network {
		t.Fatal("replacement runtime/command/network differs")
	}
	res, e := engine.Get("http://docker/containers/" + old + "/json")
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatal("original incoming container still exists", res.StatusCode)
	}
	*ids = []string{next}
	t.Log("actual deployment recreated sole running container, removed prior ID and retained verified network", command)
}
