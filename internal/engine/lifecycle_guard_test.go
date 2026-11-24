package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type guardEngine struct {
	Engine
	info               *ContainerInfo
	inspectErr         error
	started, restarted string
}

func (e *guardEngine) InspectContainer(context.Context, string) (*ContainerInfo, error) {
	return e.info, e.inspectErr
}
func (e *guardEngine) StartContainer(_ context.Context, id string) error { e.started = id; return nil }
func (e *guardEngine) RestartContainer(_ context.Context, id string) error {
	e.restarted = id
	return nil
}

type restartReinspectionEngine struct {
	guardEngine
	next *ContainerInfo
}

func (e *restartReinspectionEngine) InspectContainer(_ context.Context, id string) (*ContainerInfo, error) {
	if id == "mutable-name" {
		return e.info, nil
	}
	return e.next, nil
}

func TestLifecycleGuardRejectsDillMarkerInEitherRestartObservation(t *testing.T) {
	ordinary := &ContainerInfo{ID: "immutable-id"}
	marked := &ContainerInfo{ID: "immutable-id", Labels: map[string]string{"dill.service": ""}}
	for _, pair := range [][2]*ContainerInfo{{marked, ordinary}, {ordinary, marked}} {
		e := &restartReinspectionEngine{guardEngine: guardEngine{info: pair[0]}, next: pair[1]}
		g := &LifecycleGuard{Engine: e}
		if err := g.RestartContainer(context.Background(), "mutable-name"); !errors.Is(err, ErrManagedRestart) || e.restarted != "" {
			t.Fatalf("restart error %v, called with %q", err, e.restarted)
		}
	}
}

func TestLifecycleGuardWaitsForDeploymentBeforeClassification(t *testing.T) {
	var lock sync.Mutex
	lock.Lock()
	enabled := false
	e := &guardEngine{info: &ContainerInfo{ID: "immutable-id", Stack: "demo", Labels: map[string]string{"dill.managed": "true", "dill.stack": "demo", "dill.service": "web"}}}
	g := &LifecycleGuard{Engine: e, LockStack: func(context.Context, string) (func(), error) { lock.Lock(); return lock.Unlock, nil }, SecretEnabled: func(string) (bool, error) { return enabled, nil }}
	done := make(chan error, 1)
	go func() { done <- g.StartContainer(context.Background(), "name") }()
	select {
	case <-done:
		t.Fatal("direct start bypassed deployment lock")
	case <-time.After(30 * time.Millisecond):
	}
	enabled = true
	lock.Unlock()
	select {
	case err := <-done:
		if err == nil || e.started != "" {
			t.Fatal("new secret record ignored")
		}
	case <-time.After(time.Second):
		t.Fatal("direct start blocked")
	}
}

func TestLifecycleGuard(t *testing.T) {
	ctx := context.Background()
	managed := func() *ContainerInfo {
		return &ContainerInfo{ID: "immutable-id", Stack: "demo", Labels: map[string]string{"dill.managed": "true", "dill.stack": "demo", "dill.service": "web"}}
	}
	for _, tc := range []struct {
		name       string
		info       *ContainerInfo
		inspectErr error
		recorded   bool
		lookupErr  error
		allow      bool
	}{
		{"ordinary", &ContainerInfo{ID: "ordinary"}, nil, false, nil, true}, {"managed", managed(), nil, false, nil, false}, {"record", managed(), nil, true, nil, false}, {"record-error", managed(), nil, false, errors.New("bad"), false}, {"ambiguous", &ContainerInfo{ID: "id", Stack: "demo", Labels: map[string]string{"dill.stack": "demo"}}, nil, false, nil, false}, {"missing-id", &ContainerInfo{}, nil, false, nil, false}, {"inspect", nil, errors.New("synthetic"), false, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &guardEngine{info: tc.info, inspectErr: tc.inspectErr}
			g := &LifecycleGuard{Engine: e, SecretEnabled: func(string) (bool, error) { return tc.recorded, tc.lookupErr }}
			err := g.StartContainer(ctx, "mutable-name")
			if (err == nil) != tc.allow {
				t.Fatalf("start %v", err)
			}
			if tc.name == "managed" && !errors.Is(err, ErrManagedStart) {
				t.Fatalf("managed start denial: %v", err)
			}
			err = g.RestartContainer(ctx, "mutable-name")
			allowRestart := tc.name == "ordinary"
			if (err == nil) != allowRestart || (!allowRestart && e.restarted != "") {
				t.Fatalf("restart %v, called with %q", err, e.restarted)
			}
			if tc.name == "managed" && !errors.Is(err, ErrManagedRestart) {
				t.Fatalf("managed restart denial: %v", err)
			}
			if allowRestart && e.restarted != tc.info.ID {
				t.Fatal("restart did not use immutable ID")
			}
			if tc.allow && e.started != tc.info.ID {
				t.Fatal("not immutable ID")
			}
			if !tc.allow && (e.started != "" || e.restarted != "") {
				t.Fatal("mutated denied container")
			}
		})
	}
	for _, value := range []string{"true", "false", ""} {
		e := &guardEngine{info: managed()}
		e.info.Labels["dill.secret-enabled"] = value
		g := &LifecycleGuard{Engine: e}
		if g.RestartContainer(ctx, "x") == nil {
			t.Fatal("marker permitted")
		}
	}
}
