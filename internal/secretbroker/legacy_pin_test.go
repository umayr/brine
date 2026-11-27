package secretbroker

import (
	"strings"
	"testing"
	"time"
)

func TestLegacyPinExcludesFirstRegistrationAcrossHandles(t *testing.T) {
	path := statePath(t)
	s := provisionStore(t, path)
	other := openStore(t, path)
	scope := Scope{RegistrationID: strings.Repeat("a", 32), StackName: "demo", TargetRegistration: "local"}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() { done <- s.WithUnprotected("demo", func() { close(entered); <-release }) }()
	<-entered
	registered := make(chan error, 1)
	go func() { registered <- other.Register(scope, "admin") }()
	select {
	case <-registered:
		t.Fatal("registration raced legacy dispatch")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-registered; err != nil {
		t.Fatal(err)
	}
	called := false
	if err := s.WithUnprotected("demo", func() { called = true }); err == nil || called {
		t.Fatal("registered stack admitted ordinary submission")
	}
	if _, err := s.Save(scope, 0, "admin", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.WithUnprotected("demo", func() { called = true }); err == nil || called {
		t.Fatal("empty bindings erased protection")
	}
}
