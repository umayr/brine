package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateGitAttempt(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		cancel, conflict, fail bool
	}{
		{name: "success"}, {name: "cancel", cancel: true}, {name: "conflict", conflict: true}, {name: "failure", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			st, err := NewFSStore(root)
			if err != nil {
				t.Fatal(err)
			}
			final := filepath.Join(root, "stacks", "demo")
			if tc.conflict {
				if err := os.Mkdir(final, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(final, "keep"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stack, err := st.CreateGitAttempt(ctx, "demo", &GitSource{URL: "test", Folder: "nested"}, "custom.pkl", func(ctx context.Context, dest string) error {
				if err := os.MkdirAll(filepath.Join(dest, "nested"), 0700); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(dest, "nested", "custom.pkl"), []byte("ok"), 0600); err != nil {
					return err
				}
				if tc.cancel {
					cancel()
				}
				if tc.fail {
					return errors.New("clone failed")
				}
				return nil
			})
			if tc.cancel || tc.conflict || tc.fail {
				if err == nil {
					t.Fatal("expected error")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				want := filepath.Join(final, "repo", "nested", "custom.pkl")
				if stack.PklPath != want {
					t.Fatalf("path %q, want %q", stack.PklPath, want)
				}
				got, err := st.Get("demo")
				if err != nil || got.PklPath != want {
					t.Fatalf("get: %#v %v", got, err)
				}
			}
			if tc.conflict {
				b, err := os.ReadFile(filepath.Join(final, "keep"))
				if err != nil || string(b) != "keep" {
					t.Fatalf("existing data changed: %q %v", b, err)
				}
			} else if tc.cancel || tc.fail {
				if _, err := os.Lstat(final); !os.IsNotExist(err) {
					t.Fatalf("final path exists: %v", err)
				}
			}
			entries, err := os.ReadDir(filepath.Join(root, "stacks"))
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if e.Name() != "demo" {
					t.Fatalf("staging residue: %s", e.Name())
				}
			}
		})
	}
}

func TestGitAttemptCancelsWhileStoreLockHeld(t *testing.T) {
	root := t.TempDir()
	st, err := NewFSStore(root)
	if err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cloneDone := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := st.CreateGitAttempt(ctx, "demo", &GitSource{URL: "synthetic"}, "", func(ctx context.Context, dest string) error {
			if err := os.MkdirAll(dest, 0700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dest, "compose.pkl"), []byte("ok"), 0600); err != nil {
				return err
			}
			close(cloneDone)
			return nil
		})
		done <- err
	}()
	select {
	case <-cloneDone:
	case <-time.After(time.Second):
		t.Fatal("clone did not reach staging")
	}
	// Wait for registration in the private staging store while the real store
	// remains locked; the attempt cannot publish until that lock is released.
	deadline := time.Now().Add(time.Second)
	for {
		matches, err := filepath.Glob(filepath.Join(root, "stacks", ".git-attempt-*", "stacks", "demo", "meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("staged metadata not created")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked on store lock")
	}
	if _, err := os.Lstat(filepath.Join(root, "stacks", "demo")); !os.IsNotExist(err) {
		t.Fatalf("unexpected publication: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "stacks"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging residue: %v %v", entries, err)
	}
}
