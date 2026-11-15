package git

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakePullRunner struct {
	changed   bool
	pullErr   error
	ref       string
	refErr    error
	refCalled bool
}

func (r *fakePullRunner) Pull(context.Context, string) (bool, error) { return r.changed, r.pullErr }
func (r *fakePullRunner) ShortRef(context.Context, string) (string, error) {
	r.refCalled = true
	return r.ref, r.refErr
}

type fakePullStatus struct {
	name, commit, syncErr string
	at                    time.Time
	err                   error
}

func (s *fakePullStatus) UpdateGitStatus(name, commit, syncErr string, at time.Time) error {
	s.name, s.commit, s.syncErr, s.at = name, commit, syncErr, at
	return s.err
}

func TestPullAndRecord(t *testing.T) {
	pullErr := errors.New("pull failed")
	for _, tc := range []struct {
		name                    string
		runner                  fakePullRunner
		statusErr               error
		wantChanged             bool
		wantCommit, wantSyncErr string
		wantErr                 error
		wantRef                 bool
	}{
		{name: "changed", runner: fakePullRunner{changed: true, ref: "abc"}, wantChanged: true, wantCommit: "abc", wantRef: true},
		{name: "unchanged", runner: fakePullRunner{ref: "abc"}, wantCommit: "abc", wantRef: true},
		{name: "pull failure", runner: fakePullRunner{pullErr: pullErr}, wantErr: pullErr, wantSyncErr: "pull failed"},
		{name: "ref unavailable", runner: fakePullRunner{changed: true, refErr: errors.New("unavailable")}, wantChanged: true, wantRef: true},
		{name: "status unavailable", runner: fakePullRunner{changed: true, ref: "abc"}, statusErr: errors.New("unavailable"), wantChanged: true, wantCommit: "abc", wantRef: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := &fakePullStatus{err: tc.statusErr}
			before := time.Now()
			changed, commit, err := PullAndRecord(context.Background(), &tc.runner, status, "demo", "/repo")
			if changed != tc.wantChanged || commit != tc.wantCommit || !errors.Is(err, tc.wantErr) || tc.runner.refCalled != tc.wantRef {
				t.Fatalf("result changed=%v commit=%q err=%v refCalled=%v", changed, commit, err, tc.runner.refCalled)
			}
			if status.name != "demo" || status.commit != tc.wantCommit || status.syncErr != tc.wantSyncErr || status.at.Before(before) || status.at.After(time.Now()) {
				t.Fatalf("status: %+v", status)
			}
		})
	}
}
