package server

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"testing"
)

// The browser harness has exclusive, randomly named disposable runtime scope.
// Stop/join the agent first; even an uncertain response may have created a new
// container. Observe and pin IDs, not deletion by name or a failed receipt.
func collectIncomingCleanup(t *testing.T, stack string, engine *http.Client, ids *[]string, originalNetwork *string) []string {
	t.Helper()
	known := []string{}
	if *originalNetwork != "" {
		known = append(known, *originalNetwork)
	}
	safe := regexp.MustCompile(`^[a-f0-9]{64}$`)
	get := func(path string) []byte {
		r, e := engine.Get("http://docker" + path)
		if e != nil {
			t.Error("cleanup observation unavailable")
			return nil
		}
		defer r.Body.Close()
		b, e := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if e != nil || r.StatusCode != 200 {
			t.Error("cleanup observation unavailable", r.StatusCode)
			return nil
		}
		return b
	}
	var rows []struct {
		ID     string `json:"Id"`
		Labels map[string]string
	}
	if json.Unmarshal(get("/containers/json?all=1"), &rows) != nil {
		t.Error("cleanup inventory unavailable")
		return known
	}
	seen := map[string]bool{}
	for _, id := range *ids {
		seen[id] = true
	}
	nets := map[string]bool{}
	if *originalNetwork != "" {
		nets[*originalNetwork] = true
	}
	for _, r := range rows {
		if r.Labels["dill.stack"] != stack || r.Labels["dill.managed"] != "true" {
			continue
		}
		if !safe.MatchString(r.ID) {
			t.Error("cleanup identity invalid")
			continue
		}
		if !seen[r.ID] {
			*ids = append(*ids, r.ID)
			seen[r.ID] = true
		}
		var item struct {
			ID              string `json:"Id"`
			NetworkSettings struct {
				Networks map[string]struct{ NetworkID string }
			}
		}
		if json.Unmarshal(get("/containers/"+r.ID+"/json"), &item) != nil || item.ID != r.ID {
			continue
		}
		attached, ok := item.NetworkSettings.Networks[stack]
		if !ok || !safe.MatchString(attached.NetworkID) {
			continue
		}
		var n struct {
			ID   string `json:"Id"`
			Name string
		}
		if json.Unmarshal(get("/networks/"+attached.NetworkID), &n) == nil && n.ID == attached.NetworkID && n.Name == stack {
			nets[n.ID] = true
		}
	}
	out := []string{}
	for id := range nets {
		out = append(out, id)
	}
	return out
}
