//go:build linux || darwin

package store

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestRecipePublishFailureRetry(t *testing.T) {
	dir := t.TempDir()
	_, err := newFSRecipeStore(dir, (*FSRecipeStore).seed, func(string, string) error { return errors.New("injected publish failure") })
	if err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Lstat(filepath.Join(dir, "recipes")); !os.IsNotExist(err) {
		t.Fatalf("unexpected library: %v", err)
	}
	attempt := assertAttempts(t, dir, 1)[0]
	err = filepath.Walk(attempt, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		want := os.FileMode(0600)
		if info.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Errorf("staging %s mode %o", path, info.Mode().Perm())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewFSRecipeStore(dir); err != nil {
		t.Fatal(err)
	}
}

func TestRecipeConcurrentNoClobber(t *testing.T) {
	dir := t.TempDir()
	ready := make(chan struct{}, 2)
	goOn := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := newFSRecipeStore(dir, func(s *FSRecipeStore) error { err := s.seed(); ready <- struct{}{}; <-goOn; return err }, publishRecipes)
			errs <- err
		}()
	}
	<-ready
	<-ready
	close(goOn)
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successes %d", successes)
	}
	s, err := NewFSRecipeStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil || len(list) == 0 {
		t.Fatalf("%v %v", list, err)
	}
}

func TestRecipePermissionsAndDeletion(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFSRecipeStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "recipes")
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		want := os.FileMode(0600)
		if info.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s: %o", path, info.Mode().Perm())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil || len(list) == 0 {
		t.Fatalf("%v %v", list, err)
	}
	slug := list[0].Slug
	if err = s.Delete(slug); err != nil {
		t.Fatal(err)
	}
	s, err = NewFSRecipeStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	list, err = s.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list {
		if r.Slug == slug {
			t.Fatal("deleted recipe restored")
		}
	}
}

func TestRecipeRejectsNonDirectory(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "symlink"}[symlink], func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "recipes")
			var err error
			if symlink {
				err = os.Symlink(dir, path)
			} else {
				err = os.WriteFile(path, []byte("keep"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = NewFSRecipeStore(dir); err == nil {
				t.Fatal("accepted invalid library")
			}
		})
	}
}
