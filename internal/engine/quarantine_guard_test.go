package engine

import (
	"context"
	"github.com/umayr/brine/internal/secretbindings"
	"os"
	"testing"
	"time"
)

func TestLifecycleGuardReopenedQuarantine(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	q, err := secretbindings.NewQuarantine(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.Begin(secretbindings.DeploymentID("demo")); err != nil {
		t.Fatal(err)
	}
	q, err = secretbindings.OpenQuarantine(root)
	if err != nil {
		t.Fatal(err)
	}
	c := &secretbindings.Coordinator{}
	c.CheckAdmission = q.Check
	e := &guardEngine{info: &ContainerInfo{ID: "immutable-id", Stack: "demo", Labels: map[string]string{"dill.stack": "demo", "dill.managed": "true", "dill.service": "web"}}}
	g := &LifecycleGuard{Engine: e, LockStack: func(ctx context.Context, stack string) (func(), error) {
		return c.LockContext(ctx, secretbindings.DeploymentID(stack))
	}}
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if err := g.StartContainer(ctx, "name"); err == nil {
			t.Error("quarantined start accepted")
		}
		if err := g.RestartContainer(ctx, "name"); err == nil {
			t.Error("quarantined restart accepted")
		}
		cancel()
	}
	if e.started != "" || e.restarted != "" {
		t.Fatal("denied engine mutation ran")
	}
	if err := q.Check(secretbindings.DeploymentID("demo")); err == nil {
		t.Fatal("guard cleared intent")
	}
}
