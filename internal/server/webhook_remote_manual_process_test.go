package server

import (
	"context"
	"encoding/json"
	"github.com/umayr/brine/internal/store"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Synthetic source setup/change only. The browser must perform both production
// reviews and submissions; this helper never calls an apply or approves a review.
func setupManualBrowserQualification(t *testing.T, s *Server, fs *store.FSStore, stack string, compose, schema []byte, engine *http.Client, engineCall func(string, string) []byte, phase *atomic.Int32, browserObserved *atomic.Bool) func() {
	t.Helper()
	name := stack + "-manual"
	get := func(path string) ([]byte, int) {
		r, e := engine.Get("http://docker" + path)
		if e != nil {
			t.Fatal(e)
		}
		defer r.Body.Close()
		b, e := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if e != nil {
			t.Fatal(e)
		}
		return b, r.StatusCode
	}
	if _, status := get("/networks/" + name); status != 404 {
		t.Fatal("manual network already exists or absence unavailable", status)
	}
	var baseline []struct{ Labels map[string]string }
	if json.Unmarshal(engineCall("GET", "/containers/json?all=1"), &baseline) != nil {
		t.Fatal("manual baseline unavailable")
	}
	for _, r := range baseline {
		if r.Labels["dill.stack"] == name {
			t.Fatal("manual stack already exists")
		}
	}
	source := strings.ReplaceAll(string(compose), stack, name)
	st, e := fs.Create(name, source)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(filepath.Dir(st.PklPath), "dill.pkl"), schema, 0600); e != nil {
		t.Fatal(e)
	}
	ids := map[string]bool{}
	networkIDs := map[string]bool{}
	safeID := regexp.MustCompile(`^[a-f0-9]{64}$`)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		paths := []string{}
		for id := range ids {
			paths = append(paths, "/containers/"+id+"?force=1")
		}
		for id := range networkIDs {
			paths = append(paths, "/networks/"+id)
		}
		for _, path := range paths {
			r, _ := http.NewRequestWithContext(ctx, "DELETE", "http://docker"+path, nil)
			res, e := engine.Do(r)
			if e != nil {
				t.Error("manual fixture cleanup", e)
				continue
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if res.StatusCode >= 300 && res.StatusCode != 404 {
				t.Error("manual fixture cleanup", res.StatusCode)
			}
		}
	})
	return func() {
		completed := func(mode string) bool {
			rows, e := fs.ListManualIntents()
			if e != nil {
				t.Fatal(e)
			}
			for _, r := range rows {
				if r.Stack == name && r.Mode == mode && r.Resolved {
					return true
				}
			}
			return false
		}
		runningCount := 0
		poll := func() string {
			runningCount = 0
			var rows []struct {
				ID     string `json:"Id"`
				Labels map[string]string
				State  string
			}
			if json.Unmarshal(engineCall("GET", "/containers/json?all=1"), &rows) != nil {
				t.Fatal("manual inventory")
			}
			found := ""
			for _, r := range rows {
				if r.Labels["dill.stack"] == name && r.Labels["dill.managed"] == "true" {
					if !safeID.MatchString(r.ID) {
						t.Fatal("unsafe manual container ID")
					}
					ids[r.ID] = true
					b, status := get("/containers/" + r.ID + "/json")
					if status == 200 {
						var inspection struct {
							NetworkSettings struct {
								Networks map[string]struct{ NetworkID string }
							}
						}
						if json.Unmarshal(b, &inspection) != nil {
							t.Fatal("manual attachment unavailable")
						}
						if attachment, ok := inspection.NetworkSettings.Networks[name]; ok {
							var n struct {
								ID   string `json:"Id"`
								Name string
							}
							if !safeID.MatchString(attachment.NetworkID) || json.Unmarshal(engineCall("GET", "/networks/"+attachment.NetworkID), &n) != nil || n.ID != attachment.NetworkID || n.Name != name {
								t.Fatal("manual network attachment identity")
							}
							networkIDs[n.ID] = true
						}
					} else if status != 404 {
						t.Fatal("manual container inspection unavailable", status)
					}
					if r.State == "running" {
						runningCount++
						found = r.ID
					}
				}
			}

			return found
		}
		wait := func(mode, old string) string {
			deadline := time.Now().Add(45 * time.Second)
			for time.Now().Before(deadline) {
				id := poll()
				if id != "" && id != old && completed(mode) {
					return id
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatal("manual browser operation did not complete", mode)
			return ""
		}
		original := wait("create", "")
		changed := strings.ReplaceAll(source, "sleep 1", "sleep 2")
		if changed == source {
			t.Fatal("runtime fixture change absent")
		}
		if _, e := fs.Update(name, changed); e != nil {
			t.Fatal(e)
		}
		phase.Store(1)
		updated := wait("update", original)
		if id := poll(); id != updated || runningCount != 1 {
			t.Fatal("manual replacement is not sole running container", runningCount)
		}
		if _, status := get("/containers/" + original + "/json"); status != 404 {
			t.Fatal("original manual container was not removed", status)
		}
		var inspection struct {
			ID     string `json:"Id"`
			State  struct{ Running bool }
			Config struct{ Cmd []string }
		}
		if json.Unmarshal(engineCall("GET", "/containers/"+updated+"/json"), &inspection) != nil || inspection.ID != updated || !inspection.State.Running || !strings.Contains(strings.Join(inspection.Config.Cmd, " "), "sleep 2") {
			t.Fatal("manual update did not recreate expected running command")
		}
		if original == updated {
			t.Fatal("manual runtime update reused old identity")
		}
		count := 0
		for _, event := range journalEvents(t, s) {
			if event.Stack.Name == name && event.Action == "deploy" {
				count++
				if event.Type != "operation.completed" || event.Outcome != "success" || event.Effects != "complete" || event.Source.Kind != "workspace" || event.Target.Kind != "remote" {
					t.Fatal("manual producer lost actual result", event)
				}
			}
		}
		if count != 2 {
			t.Fatal("manual operations duplicated/lost", count)
		}
		phase.Store(2)
		deadline := time.Now().Add(15 * time.Second)
		for !browserObserved.Load() && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if !browserObserved.Load() {
			t.Fatal("browser did not observe verified manual update")
		}
		t.Log("real manual browser review/create/update recreated running container; exactly two deploy journals")
	}
}
