package remoteui

import (
	"errors"
	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/store"
	"strings"
	"testing"
)

type failedOutcomeStore struct {
	store.RemoteGitStore
	err error
}

func (s failedOutcomeStore) SwapRemoteGit(string, string, *store.RemoteGitPolicy, *store.RemoteGitPolicy) error {
	return s.err
}
func TestKnownGitOutcomeReturnsRecoveryWriteError(t *testing.T) {
	failure := errors.New("persistence failed")
	h := &Deployment{gitStore: failedOutcomeStore{err: failure}}
	s := &store.Stack{Name: "demo"}
	p := &store.RemoteGitPolicy{Source: strings.Repeat("a", 64), Request: strings.Repeat("b", 32), Journal: strings.Repeat("c", 32), Phase: "pending"}
	status := agentclient.DeploymentStatus{RequestID: p.Request, Stack: s.Name, State: "succeeded", Effects: "complete"}
	if e := h.finishGit(s, p, status); !errors.Is(e, failure) {
		t.Fatal("known outcome persistence error lost", e)
	}
}
