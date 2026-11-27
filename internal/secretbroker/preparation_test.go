package secretbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/umayr/brine/internal/secrets"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeAcquirer struct {
	value string
	err   error
	calls int
}

func (f *fakeAcquirer) Acquire(context.Context, secrets.Selector) (secrets.Value, error) {
	f.calls++
	if f.err != nil {
		return secrets.Value{}, f.err
	}
	return secrets.NewValue([]byte(f.value))
}
func reviewFixture(t *testing.T) (*Store, *Manager, *fakeAcquirer, ReviewIdentity, string) {
	t.Helper()
	path := statePath(t)
	s := provisionStore(t, path)
	scope := demoScope()
	wantError(t, s.Register(scope, "admin"), nil)
	_, e := s.Save(scope, 0, "admin", []Binding{demoBinding()})
	wantError(t, e, nil)
	f := &fakeAcquirer{value: "synthetic-private-generation-unique"}
	m, e := NewManager(s, f, func(context.Context, ReviewIdentity, []Binding) error { return nil })
	wantError(t, e, nil)
	t.Cleanup(m.Close)
	d := "sha256:" + strings.Repeat("a", 64)
	i := ReviewIdentity{Scope: scope, SourceDigest: d, TargetDigest: d, PolicyRevision: d, SessionDigest: d, BindingRevision: 1}
	return s, m, f, i, path
}
func TestPreparedGenerationFrozenScopedSingleUse(t *testing.T) {
	_, m, f, i, path := reviewFixture(t)
	meta, e := m.Prepare(context.Background(), i, []string{"TOKEN"})
	wantError(t, e, nil)
	f.value = "changed-after-review"
	wrong := i
	wrong.SessionDigest = "sha256:" + strings.Repeat("b", 64)
	_, e = m.Inspect(context.Background(), meta.ID, wrong)
	wantError(t, e, ErrUnavailable)
	_, delivery, e := m.Take(context.Background(), meta.ID, i)
	wantError(t, e, nil)
	defer delivery.Close()
	wantError(t, delivery.Each(func(name string, b []byte) error {
		if name != "TOKEN" || string(b) != "synthetic-private-generation-unique" {
			t.Fatal("not exact prepared bytes")
		}
		return nil
	}), nil)
	if raw, e := json.Marshal(delivery); e == nil || len(raw) != 0 {
		t.Fatal("delivery serializable")
	}
	if strings.Contains(fmt.Sprintf("%+v", delivery), "synthetic-private") {
		t.Fatal("delivery printable")
	}
	_, _, e = m.Take(context.Background(), meta.ID, i)
	wantError(t, e, ErrUnavailable)
	raw, e := os.ReadFile(path)
	wantError(t, e, nil)
	if bytes.Contains(raw, []byte("synthetic-private-generation-unique")) {
		t.Fatal("plaintext persisted in reference store")
	}
}
func TestPreparationDenialsStalenessAndExpiry(t *testing.T) {
	s, m, f, i, _ := reviewFixture(t)
	_, e := m.Prepare(context.Background(), i, []string{"OTHER"})
	wantError(t, e, ErrInvalidInput)
	if f.calls != 0 {
		t.Fatal("unexpected acquisition")
	}
	m.authorize = func(context.Context, ReviewIdentity, []Binding) error { return errors.New("private denial") }
	_, e = m.Prepare(context.Background(), i, []string{"TOKEN"})
	wantError(t, e, secrets.ErrDenied)
	if f.calls != 0 {
		t.Fatal("unauthorized acquisition")
	}
	m.authorize = func(context.Context, ReviewIdentity, []Binding) error { return nil }
	meta, e := m.Prepare(context.Background(), i, []string{"TOKEN"})
	wantError(t, e, nil)
	_, e = s.Save(i.Scope, 1, "admin", nil)
	wantError(t, e, nil)
	_, _, e = m.Take(context.Background(), meta.ID, i)
	wantError(t, e, ErrConflict)
	wantError(t, m.Cancel(meta.ID, i), nil)
	i.BindingRevision = 2
	_, e = m.Prepare(context.Background(), i, []string{"TOKEN"})
	wantError(t, e, ErrInvalidInput)
}
func TestPreparationExpiresRevokesAndSanitizes(t *testing.T) {
	_, m, f, i, _ := reviewFixture(t)
	clock := time.Now()
	m.now = func() time.Time { return clock }
	meta, e := m.Prepare(context.Background(), i, []string{"TOKEN"})
	wantError(t, e, nil)
	clock = clock.Add(6 * time.Minute)
	_, e = m.Inspect(context.Background(), meta.ID, i)
	wantError(t, e, ErrUnavailable)
	meta, e = m.Prepare(context.Background(), i, []string{"TOKEN"})
	wantError(t, e, nil)
	m.authorize = func(context.Context, ReviewIdentity, []Binding) error { return errors.New("private") }
	_, _, e = m.Take(context.Background(), meta.ID, i)
	wantError(t, e, secrets.ErrDenied)
	m.authorize = func(context.Context, ReviewIdentity, []Binding) error { return nil }
	f.err = errors.New("secret response body must not escape")
	_, e = m.Prepare(context.Background(), i, []string{"TOKEN"})
	wantError(t, e, secrets.ErrUnavailable)
	if strings.Contains(e.Error(), "response body") {
		t.Fatal("raw error leaked")
	}
}
