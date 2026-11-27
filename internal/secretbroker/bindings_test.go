package secretbroker

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func statePath(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "bindings.sqlite")
}

func provisionStore(t *testing.T, path string) *Store {
	t.Helper()
	wantError(t, Provision(path), nil)
	return openStore(t, path)
}

func openStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	wantError(t, err, nil)
	t.Cleanup(func() { wantError(t, s.Close(), nil) })
	return s
}

func wantError(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}

func rejectOpen(t *testing.T, path string, want error) {
	t.Helper()
	s, err := Open(path)
	if s != nil {
		_ = s.Close()
		t.Fatal("unexpected open store")
	}
	wantError(t, err, want)
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	_, err := os.Lstat(path)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expected no replacement state file")
	}
}

func demoScope() Scope {
	return Scope{RegistrationID: "0123456789abcdef0123456789abcdef", StackName: "test-stack", TargetRegistration: "test-target"}
}

func demoBinding() Binding {
	return Binding{Name: "TOKEN", ConnectionID: "vault-connection", Kind: ProviderVault,
		Vault: VaultRef{Path: "synthetic/demo", Property: "test-property", Version: 9}}
}

func TestProvisionAndOpen(t *testing.T) {
	path := filepath.Join(filepath.Dir(statePath(t)), "bindings?#%.sqlite")
	rejectOpen(t, path, ErrUnavailable)
	assertMissing(t, path)
	s := provisionStore(t, path)
	info, err := os.Lstat(path)
	wantError(t, err, nil)
	if info.Mode() != 0600 {
		t.Fatalf("state mode = %v, want 0600 regular file", info.Mode())
	}
	id := s.Identity()
	if !validID(id) {
		t.Fatal("invalid durable identity")
	}
	wantError(t, Provision(path), ErrExists)
	wantError(t, s.Close(), nil)
	if openStore(t, path).Identity() != id {
		t.Fatal("identity did not survive re-open")
	}
	other := provisionStore(t, filepath.Join(filepath.Dir(path), "other.sqlite"))
	if other.Identity() == id {
		t.Fatal("separate provisioned stores share an identity")
	}
}

func TestUnsafePathsRefused(t *testing.T) {
	path := statePath(t)
	dir := filepath.Dir(path)
	wantError(t, Provision(filepath.Base(path)), ErrUnsafePath)
	wantError(t, Provision(dir+"/./state.sqlite"), ErrUnsafePath)
	wantError(t, Provision(filepath.Join(dir, "missing-dir", "state.sqlite")), ErrUnsafePath)
	wantError(t, os.Chmod(dir, 0755), nil)
	wantError(t, Provision(path), ErrUnsafePath)
	assertMissing(t, path)
	wantError(t, os.Chmod(dir, 0700), nil)
	wantError(t, Provision(path), nil)
	wantError(t, os.Chmod(path, 0644), nil)
	rejectOpen(t, path, ErrUnsafePath)
	wantError(t, os.Chmod(path, 0600), nil)

	link := filepath.Join(dir, "file-link")
	wantError(t, os.Symlink(path, link), nil)
	rejectOpen(t, link, ErrUnsafePath)
	wantError(t, Provision(link), ErrExists)
	rootLink := filepath.Join(dir, "dir-link")
	wantError(t, os.Symlink(dir, rootLink), nil)
	rejectOpen(t, filepath.Join(rootLink, filepath.Base(path)), ErrUnsafePath)
	wantError(t, Provision(filepath.Join(rootLink, "new.sqlite")), ErrUnsafePath)

	hardlink := filepath.Join(dir, "hard-link")
	wantError(t, os.Link(path, hardlink), nil)
	rejectOpen(t, path, ErrUnsafePath)
	wantError(t, os.Remove(hardlink), nil)
	wantError(t, os.Symlink(link, path+"-journal"), nil)
	rejectOpen(t, path, ErrUnsafePath)
}

func TestMissingCorruptAndIncompatibleStateNotRecreated(t *testing.T) {
	t.Run("missing after open", func(t *testing.T) {
		path := statePath(t)
		s := provisionStore(t, path)
		scope := demoScope()
		wantError(t, s.Register(scope, "operator"), nil)
		wantError(t, os.Rename(path, path+".retained"), nil)
		_, err := s.Load(scope)
		wantError(t, err, ErrUnavailable)
		_, err = s.Save(scope, 0, "operator", nil)
		wantError(t, err, ErrUnavailable)
		rejectOpen(t, path, ErrUnavailable)
		assertMissing(t, path)
		old := openStore(t, path+".retained")
		if old.Identity() != s.Identity() {
			t.Fatal("retained state identity changed")
		}
	})
	t.Run("corrupt", func(t *testing.T) {
		path := statePath(t)
		wantError(t, Provision(path), nil)
		broken := bytes.Repeat([]byte("not-a-database"), 100)
		wantError(t, os.WriteFile(path, broken, 0600), nil)
		rejectOpen(t, path, ErrUnavailable)
		wantError(t, Provision(path), ErrExists)
		after, err := os.ReadFile(path)
		wantError(t, err, nil)
		if !bytes.Equal(after, broken) {
			t.Fatal("invalid state was modified")
		}
	})
	t.Run("empty existing file", func(t *testing.T) {
		path := statePath(t)
		wantError(t, os.WriteFile(path, nil, 0600), nil)
		rejectOpen(t, path, ErrUnavailable)
		wantError(t, Provision(path), ErrExists)
		info, err := os.Stat(path)
		wantError(t, err, nil)
		if info.Size() != 0 {
			t.Fatal("empty existing file was initialized")
		}
	})
	for _, tc := range []struct{ name, sql string }{
		{"incorrect version", "PRAGMA user_version = 99"},
		{"incorrect schema", "CREATE TABLE extra (data TEXT)"},
		{"inconsistent snapshot", "DELETE FROM broker_revisions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := statePath(t)
			s := provisionStore(t, path)
			wantError(t, s.Register(demoScope(), "operator"), nil)
			_, err := s.state.db.Exec(tc.sql)
			wantError(t, err, nil)
			wantError(t, s.Close(), nil)
			rejectOpen(t, path, ErrUnavailable)
			wantError(t, Provision(path), ErrExists)
		})
	}
}

func TestRegistrationAndScopeDenial(t *testing.T) {
	s := provisionStore(t, statePath(t))
	scope := demoScope()
	_, err := s.Load(scope)
	wantError(t, err, ErrUnregistered)
	_, err = s.History(scope)
	wantError(t, err, ErrUnregistered)
	_, err = s.Save(scope, 0, "operator", nil)
	wantError(t, err, ErrUnregistered)
	invalid := scope
	invalid.RegistrationID = strings.ToUpper(invalid.RegistrationID)
	wantError(t, s.Register(invalid, "operator"), ErrInvalidInput)
	wantError(t, s.Register(scope, "operator"), nil)
	wantError(t, s.Register(scope, "operator"), ErrRegistrationExists)
	wrongStack, wrongTarget := scope, scope
	wrongStack.StackName = "different-stack"
	wrongTarget.TargetRegistration = "different-target"
	for _, wrong := range []Scope{wrongStack, wrongTarget} {
		wantError(t, s.Register(wrong, "operator"), ErrScopeMismatch)
		_, err := s.Load(wrong)
		wantError(t, err, ErrScopeMismatch)
		_, err = s.History(wrong)
		wantError(t, err, ErrScopeMismatch)
		_, err = s.Save(wrong, 0, "operator", nil)
		wantError(t, err, ErrScopeMismatch)
	}
	other := scope
	other.RegistrationID = strings.Repeat("a", 32)
	wantError(t, s.Register(other, "operator"), nil)
	_, err = s.Save(scope, 0, "operator", []Binding{demoBinding()})
	wantError(t, err, nil)
	snap, err := s.Load(other)
	wantError(t, err, nil)
	if snap.Scope != other || snap.Revision != 0 || len(snap.Bindings) != 0 {
		t.Fatal("binding revision crossed registration scopes")
	}
}

func TestCASImmutableHistoryAndUnbind(t *testing.T) {
	path := statePath(t)
	s := provisionStore(t, path)
	scope := demoScope()
	wantError(t, s.Register(scope, "registrar"), nil)
	vault := demoBinding()
	infisical := Binding{Name: "APP_KEY", ConnectionID: "infisical-connection", Kind: ProviderInfisical,
		Infisical: InfisicalRef{Folder: "/synthetic", Key: "sample", Version: 7}}
	local := Binding{Name: "LOCAL", ConnectionID: "local-connection", Kind: ProviderLocal, LocalAlias: "synthetic-alias"}
	input := []Binding{vault, infisical, local}
	rev, err := s.Save(scope, 0, "binder", input)
	wantError(t, err, nil)
	if rev != 1 {
		t.Fatalf("save revision = %d, want 1", rev)
	}
	input[0].Vault.Path = "edited/input"
	_, err = s.Save(scope, 0, "stale", nil)
	wantError(t, err, ErrConflict)
	first, err := s.Load(scope)
	wantError(t, err, nil)
	if first.Revision != 1 || !slices.Equal(first.Bindings, []Binding{infisical, local, vault}) {
		t.Fatal("saved binding set or order changed")
	}
	first.Bindings[0].Infisical.Key = "edited-snapshot"
	rev, err = s.Save(scope, 1, "unbinder", []Binding{})
	wantError(t, err, nil)
	if rev != 2 {
		t.Fatalf("unbind revision = %d, want 2", rev)
	}
	wantError(t, s.Close(), nil)
	s = openStore(t, path)
	current, err := s.Load(scope)
	wantError(t, err, nil)
	if current.Scope != scope || current.Revision != 2 || len(current.Bindings) != 0 {
		t.Fatal("unbind did not preserve protected scope and advance revision")
	}
	wantError(t, s.Register(scope, "registrar"), ErrRegistrationExists)
	initial, err := s.LoadRevision(scope, 0)
	wantError(t, err, nil)
	if initial.Revision != 0 || len(initial.Bindings) != 0 {
		t.Fatal("registration snapshot changed")
	}
	immutable, err := s.LoadRevision(scope, 1)
	wantError(t, err, nil)
	if immutable.Revision != 1 || !slices.Equal(immutable.Bindings, []Binding{infisical, local, vault}) {
		t.Fatal("historical snapshot changed")
	}
	_, err = s.LoadRevision(scope, 3)
	wantError(t, err, ErrRevisionNotFound)
	events, err := s.History(scope)
	wantError(t, err, nil)
	if len(events) != 3 {
		t.Fatalf("history has %d events, want 3", len(events))
	}
	for i, e := range events {
		if e.Revision != int64(i) || e.At.IsZero() ||
			e.Kind != []EventKind{EventRegister, EventSave, EventUnbind}[i] ||
			e.Actor != []string{"registrar", "binder", "unbinder"}[i] ||
			e.BindingCount != []int{0, 3, 0}[i] {
			t.Fatalf("unexpected audit metadata at revision %d", i)
		}
	}
}

func TestConcurrentCASAcrossHandles(t *testing.T) {
	path := statePath(t)
	a := provisionStore(t, path)
	b := openStore(t, path)
	scope := demoScope()
	wantError(t, a.Register(scope, "operator"), nil)
	start, results := make(chan struct{}), make(chan error, 2)
	for _, s := range []*Store{a, b} {
		go func(s *Store) {
			<-start
			_, err := s.Save(scope, 0, "operator", []Binding{demoBinding()})
			results <- err
		}(s)
	}
	close(start)
	x, y := <-results, <-results
	if !(x == nil && y == ErrConflict || y == nil && x == ErrConflict) {
		t.Fatalf("concurrent save errors = %v, %v; want one conflict", x, y)
	}
	events, err := b.History(scope)
	wantError(t, err, nil)
	if len(events) != 2 || events[1].BindingCount != 1 {
		t.Fatal("concurrent save added incorrect audit events")
	}
}

func TestTransactionRollsBackPartialSnapshot(t *testing.T) {
	path := statePath(t)
	s := provisionStore(t, path)
	scope := demoScope()
	wantError(t, s.Register(scope, "operator"), nil)
	b := demoBinding()
	// Exercise the DB constraint after the first binding INSERT, bypassing
	// public input validation. Both the audit event and first row must roll back.
	err := s.withTx(func(tx *sql.Tx) error {
		if err := insertRevision(tx, scope, 1, "operator", 2); err != nil {
			return err
		}
		return insertBindings(tx, scope, 1, []Binding{b, b})
	})
	wantError(t, err, ErrUnavailable)
	wantError(t, s.Close(), nil)
	s = openStore(t, path)
	events, err := s.History(scope)
	wantError(t, err, nil)
	if len(events) != 1 {
		t.Fatal("aborted mutation appended an audit event")
	}
	_, err = s.Save(scope, 0, "operator", []Binding{b})
	wantError(t, err, nil)
}

func TestInputBoundsAndHistoryCapacity(t *testing.T) {
	s := provisionStore(t, statePath(t))
	scope := demoScope()
	wantError(t, s.Register(scope, "operator"), nil)
	b := demoBinding()
	_, err := s.Save(scope, 0, "operator", []Binding{b, b})
	wantError(t, err, ErrInvalidInput)
	_, err = s.Save(scope, 0, "operator", make([]Binding, MaxBindingsPerRevision+1))
	wantError(t, err, ErrCapacity)
	for _, bad := range []func(*Binding){
		func(b *Binding) { b.Name = strings.Repeat("b", maxNameLength+1) },
		func(b *Binding) { b.Vault.Path = strings.Repeat("b", maxReferenceLength+1) },
		func(b *Binding) { b.Vault.Path = "https://example.invalid/token" },
		func(b *Binding) { b.Vault.Version = -1 },
		func(b *Binding) { b.LocalAlias = "wrong-kind" },
	} {
		invalid := b
		bad(&invalid)
		_, err = s.Save(scope, 0, "operator", []Binding{invalid})
		wantError(t, err, ErrInvalidInput)
	}
	_, err = s.Save(scope, 0, "operator", []Binding{b})
	wantError(t, err, nil)
	for rev := int64(1); rev < MaxRevisionsPerScope-1; rev++ {
		_, err := s.Save(scope, rev, "operator", nil)
		wantError(t, err, nil)
	}
	_, err = s.Save(scope, MaxRevisionsPerScope-1, "operator", nil)
	wantError(t, err, ErrCapacity)
	events, err := s.History(scope)
	wantError(t, err, nil)
	if len(events) != MaxRevisionsPerScope {
		t.Fatal("capacity failure changed audit history")
	}
	first, err := s.LoadRevision(scope, 1)
	wantError(t, err, nil)
	if !slices.Equal(first.Bindings, []Binding{b}) {
		t.Fatal("capacity failure discarded an old snapshot")
	}
}

func TestFormattingRedactsMetadata(t *testing.T) {
	scope, binding := demoScope(), demoBinding()
	event := AuditEvent{Actor: "synthetic-identity"}
	inf := InfisicalRef{Folder: "/synthetic-infisical", Key: "test-key"}
	items := []any{scope, binding, &binding, binding.Vault, inf, event, Snapshot{Scope: scope, Bindings: []Binding{binding}}}
	for _, output := range []string{fmt.Sprint(items), fmt.Sprintf("%+v", items), fmt.Sprintf("%#v", items), fmt.Sprintf("%s", items)} {
		for _, field := range []string{scope.RegistrationID, scope.StackName, scope.TargetRegistration,
			binding.Name, binding.ConnectionID, binding.Vault.Path, binding.Vault.Property, event.Actor, inf.Folder, inf.Key} {
			if strings.Contains(output, field) {
				t.Fatal("formatting disclosed sensitive metadata")
			}
		}
	}
}
