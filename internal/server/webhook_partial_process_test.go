package server

import (
	"context"
	"encoding/json"
	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/store"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

func qualifyPartialUpdate(t *testing.T, ctx context.Context, s *Server, fs *store.FSStore, name string, fault *engineStartFault, engine *http.Client, call func(string, string) []byte, status func(store.ManualDeploymentIntent) (agentclient.DeploymentStatus, error)) {
	t.Helper()
	pinned := map[string]bool{}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		ids := []string{}
		for id := range pinned {
			ids = append(ids, id)
		}
		network := ""
		collectIncomingCleanup(t, name, engine, &ids, &network)
		for _, id := range ids {
			pinned[id] = true
		}
		for id := range pinned {
			r, _ := http.NewRequestWithContext(cleanup, "DELETE", "http://docker/containers/"+id+"?force=1", nil)
			res, e := engine.Do(r)
			if e != nil {
				t.Error("partial cleanup unavailable")
				continue
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if res.StatusCode >= 300 && res.StatusCode != 404 {
				t.Error("partial cleanup failed", res.StatusCode)
			}
		}
	})
	observe := func() (string, bool, []string) {
		var rows []struct {
			ID     string `json:"Id"`
			Labels map[string]string
		}
		if json.Unmarshal(call("GET", "/containers/json?all=1"), &rows) != nil {
			t.Fatal("partial inventory unavailable")
		}
		ids := []string{}
		for _, r := range rows {
			if r.Labels["dill.stack"] == name && r.Labels["dill.managed"] == "true" {
				if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(r.ID) {
					t.Fatal("unsafe partial ID")
				}
				pinned[r.ID] = true
				ids = append(ids, r.ID)
			}
		}
		if len(ids) == 0 {
			return "", false, nil
		}
		if len(ids) != 1 {
			t.Fatal("partial runtime membership differs", len(ids))
		}
		var item struct {
			ID     string `json:"Id"`
			State  struct{ Running bool }
			Config struct{ Cmd []string }
		}
		if json.Unmarshal(call("GET", "/containers/"+ids[0]+"/json"), &item) != nil || item.ID != ids[0] {
			t.Fatal("partial inspection unavailable")
		}
		return item.ID, item.State.Running, item.Config.Cmd
	}
	attempts := fault.Attempts.Load()
	old, running, _ := observe()
	if !running {
		t.Fatal("partial fixture not initially running")
	}
	source, e := fs.ReadPkl(name)
	if e != nil {
		t.Fatal(e)
	}
	changed := strings.ReplaceAll(source, "sleep 2", "sleep 5")
	if changed == source {
		t.Fatal("partial source change absent")
	}
	if _, e = fs.Update(name, changed); e != nil {
		t.Fatal(e)
	}
	fault.Armed.Store(true)
	fault.Phase.Store(5)
	var intent store.ManualDeploymentIntent
	var actual agentclient.DeploymentStatus
	done := false
	for end := time.Now().Add(45 * time.Second); time.Now().Before(end); {
		rows, e := fs.ListManualIntents()
		if e != nil {
			t.Fatal(e)
		}
		for _, r := range rows {
			if r.Stack == name && r.Mode == "update" && !r.Resolved {
				result, e := status(r)
				if e == nil && result.State == "unknown" {
					intent, actual = r, result
					done = true
					break
				}
			}
		}
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !done || actual.Effects != "unknown" || fault.Denied.Load() != 1 || fault.Attempts.Load() != attempts+1 {
		t.Fatal("actual engine outage did not retain its reported unknown result", actual.State, actual.Effects, fault.Denied.Load())
	}
	fault.Armed.Store(false)
	replacement, running, cmd := observe()
	if replacement != "" || running || len(cmd) != 0 {
		t.Fatal("engine failure did not leave expected absent runtime")
	}
	deniedID, ok := fault.Container.Load().(string)
	if !ok || deniedID == old || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(deniedID) {
		t.Fatal("denied start identity unavailable")
	}
	pinned[deniedID] = true
	failedContainer, e := engine.Get("http://docker/containers/" + deniedID + "/json")
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, failedContainer.Body)
	failedContainer.Body.Close()
	if failedContainer.StatusCode != 404 {
		t.Fatal("failed replacement was not cleaned by target")
	}
	res, e := engine.Get("http://docker/containers/" + old + "/json")
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatal("partial update did not remove original")
	}
	fault.Request.Store(intent.Request)
	fault.Phase.Store(6)
	for end := time.Now().Add(40 * time.Second); !fault.Observed.Load() && time.Now().Before(end); {
		time.Sleep(20 * time.Millisecond)
	}
	if !fault.Observed.Load() {
		t.Fatal("browser did not inspect known partial failure")
	}
	saved, e := fs.GetManualIntent(intent.Request)
	latest, e2 := status(intent)
	id, nowRunning, nowCmd := observe()
	if e != nil || saved.Resolved || e2 != nil || latest.State != "unknown" || latest.Effects != "unknown" || id != replacement || nowRunning || len(nowCmd) != 0 || fault.Denied.Load() != 1 || fault.Attempts.Load() != attempts+1 {
		t.Fatal("manual inspection retried/repaired/rewrote partial outcome")
	}
	sourceAfter, e := fs.ReadPkl(name)
	if e != nil || sourceAfter != changed {
		t.Fatal("partial inspection rewrote source")
	}
	partialFact := false
	distinct := map[string]bool{}
	unknownCount := 0
	expected := hookDigest("remote_op_", intent.Target+"\x00"+intent.Request)
	count := 0
	for _, event := range journalEvents(t, s) {
		if event.Stack.Name == name && event.Action == "deploy" {
			count++
			if event.OperationID == "" || distinct[event.OperationID] {
				t.Fatal("partial journal identity unavailable")
			}
			distinct[event.OperationID] = true
			if event.Outcome == "unknown" {
				unknownCount++
				if event.OperationID == expected && event.Effects == "unknown" {
					partialFact = true
				}
			}
		}
	}
	if count != 3 || !partialFact || unknownCount != 1 {
		t.Fatal("partial operation facts lost/duplicated", count)
	}
	t.Log("real engine start outage removed original and target cleaned failed replacement; reported unknown stayed unresolved, browser inspection did not retry or repair and redeploy remained blocked")
}
