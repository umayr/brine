package remoteui

import (
	"fmt"
	"sync"
	"testing"
)

func TestDeploymentAdmissionIsolationAndBound(t *testing.T) {
	var gate deploymentAdmission
	a, ok := gate.try("a")
	if !ok {
		t.Fatal("first admission")
	}
	if _, ok := gate.try("a"); ok {
		t.Fatal("same-stack overlap admitted")
	}
	var releases []func()
	for _, name := range []string{"b", "c", "d"} {
		release, ok := gate.try(name)
		if !ok {
			t.Fatal("unrelated stack blocked")
		}
		releases = append(releases, release)
	}
	if _, ok := gate.try("e"); ok {
		t.Fatal("global bound exceeded")
	}
	a()
	e, ok := gate.try("e")
	if !ok {
		t.Fatal("released capacity unavailable")
	}
	e()
	for _, release := range releases {
		release()
	}
	if len(gate.active) != 0 {
		t.Fatal("idle keys retained")
	}
}

func TestDeploymentAdmissionConcurrentExclusion(t *testing.T) {
	var gate deploymentAdmission
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				release, ok := gate.try(fmt.Sprintf("stack-%d", i%3))
				if ok {
					release()
				}
			}
		}(i)
	}
	wg.Wait()
	if len(gate.active) != 0 {
		t.Fatal("active entries leaked")
	}
}
