//go:build linux

package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/store"
)

const (
	sourceTestName     = "workspace"
	sourceTestOriginal = "value = 1\n"
	sourceTestEdited   = "value = 2\n"
)

func sourceTestOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func sourceTestWorkspace(t *testing.T) (*store.FSStore, string, string) {
	t.Helper()
	dataDir := t.TempDir()
	s, err := store.NewFSStore(dataDir)
	sourceTestOK(t, err)
	_, err = s.Create(sourceTestName, sourceTestOriginal)
	sourceTestOK(t, err)
	stacks := filepath.Join(dataDir, "stacks")
	return s, filepath.Join(stacks, sourceTestName), filepath.Join(stacks, ".source-restore-"+sourceTestName)
}

func sourceTestReview(t *testing.T, s *store.FSStore) store.SourceView {
	t.Helper()
	v, err := s.ReviewSource(context.Background(), sourceTestName)
	sourceTestOK(t, err)
	if v.Baseline == "" {
		t.Fatal("review returned an empty baseline")
	}
	return v
}

func sourceTestRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	sourceTestOK(t, err)
	return b
}

func sourceTestAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Lstat(%s) = %v, want not exist", path, err)
	}
}

func sourceTestWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	sourceTestOK(t, os.MkdirAll(filepath.Dir(p), 0700))
	sourceTestOK(t, os.WriteFile(p, []byte(content), 0600))
}

func TestFSStoreSourceLineageBeyondOldLimit(t *testing.T) {
	ctx := context.Background()
	s, _, _ := sourceTestWorkspace(t)
	for i := 1; i <= 101; i++ {
		v := sourceTestReview(t, s)
		id := fmt.Sprintf("%064x", i)
		sourceTestOK(t, s.CommitSource(ctx, sourceTestName, v.Baseline, func(store.SourceView) (store.SourceUpdate, error) {
			return store.SourceUpdate{NewRevisions: []string{id}}, nil
		}))
	}
	if got := sourceTestReview(t, s); len(got.Revisions) != 101 || string(got.Files["compose.pkl"]) != sourceTestOriginal {
		t.Fatal("the 101st checkpoint did not persist without changing source")
	}
}

func TestFSStoreSourceCaptureBinding(t *testing.T) {
	ctx := context.Background()
	s, dir, stage := sourceTestWorkspace(t)
	captureID := strings.Repeat("a", 64)
	initial := sourceTestReview(t, s)
	if initial.Scope != "" || len(initial.Revisions) != 0 || initial.Entrypoint != "compose.pkl" ||
		!maps.EqualFunc(initial.Files, map[string][]byte{"compose.pkl": []byte(sourceTestOriginal)}, bytes.Equal) {
		t.Fatalf("initial review = %+v", initial)
	}
	if again := sourceTestReview(t, s); again.Baseline != initial.Baseline {
		t.Fatal("baseline changed without a workspace change")
	}
	initialDir, err := os.Stat(dir)
	sourceTestOK(t, err)
	var scope string
	sourceTestOK(t, s.CommitSource(ctx, sourceTestName, initial.Baseline, func(v store.SourceView) (store.SourceUpdate, error) {
		if v.Baseline != initial.Baseline || v.Scope == "" || len(v.Revisions) != 0 ||
			v.Entrypoint != initial.Entrypoint || !maps.EqualFunc(v.Files, initial.Files, bytes.Equal) {
			t.Fatalf("capture callback view = %+v, initial review = %+v", v, initial)
		}
		scope = v.Scope
		return store.SourceUpdate{NewRevisions: []string{captureID}}, nil
	}))
	captured := sourceTestReview(t, s)
	if captured.Scope != scope || !slices.Equal(captured.Revisions, []string{captureID}) || captured.Baseline == initial.Baseline ||
		captured.Entrypoint != initial.Entrypoint || !maps.EqualFunc(captured.Files, initial.Files, bytes.Equal) {
		t.Fatalf("review after capture = %+v", captured)
	}
	capturedDir, err := os.Stat(dir)
	sourceTestOK(t, err)
	if !os.SameFile(initialDir, capturedDir) {
		t.Fatal("retain-only capture replaced the workspace directory")
	}
	sourceTestAbsent(t, stage)

	// Edit only the bytes (same length, with no metadata update through the store).
	sourceTestWrite(t, dir, "compose.pkl", sourceTestEdited)
	err = s.CommitSource(ctx, sourceTestName, captured.Baseline, func(store.SourceView) (store.SourceUpdate, error) {
		t.Fatal("stale commit invoked the callback")
		return store.SourceUpdate{}, nil
	})
	if !errors.Is(err, store.ErrSourceStale) {
		t.Fatalf("CommitSource after edit = %v, want ErrSourceStale", err)
	}
	edited := sourceTestReview(t, s)
	if edited.Baseline == captured.Baseline || edited.Scope != scope || !slices.Equal(edited.Revisions, []string{captureID}) ||
		string(edited.Files["compose.pkl"]) != sourceTestEdited {
		t.Fatalf("review after rejected commit = %+v", edited)
	}
	sourceTestAbsent(t, stage)

	// A new registration of the same name does not inherit the previous lineage.
	sourceTestOK(t, s.Delete(sourceTestName))
	_, err = s.Create(sourceTestName, sourceTestOriginal)
	sourceTestOK(t, err)
	recreated := sourceTestReview(t, s)
	if recreated.Scope != "" || len(recreated.Revisions) != 0 {
		t.Fatalf("recreated workspace inherited history: %+v", recreated)
	}
	sourceTestOK(t, s.CommitSource(ctx, sourceTestName, recreated.Baseline, func(v store.SourceView) (store.SourceUpdate, error) {
		return store.SourceUpdate{NewRevisions: []string{strings.Repeat("b", 64)}}, nil
	}))
	if got := sourceTestReview(t, s).Scope; got == "" || got == scope {
		t.Fatalf("recreated workspace scope = %q, previous scope = %q", got, scope)
	}
}

func TestFSStoreSourceAutoCheckpointPreferenceDoesNotRewriteSource(t *testing.T) {
	ctx := context.Background()
	s, dir, stage := sourceTestWorkspace(t)
	initial := sourceTestReview(t, s)
	enabled := true
	if err := s.CommitSource(ctx, sourceTestName, initial.Baseline, func(store.SourceView) (store.SourceUpdate, error) {
		return store.SourceUpdate{AutoCheckpoint: &enabled}, nil
	}); !errors.Is(err, store.ErrSourceUnavailable) {
		t.Fatalf("enabled with no retained baseline: %v", err)
	}
	id := strings.Repeat("a", 64)
	sourceTestOK(t, s.CommitSource(ctx, sourceTestName, initial.Baseline, func(store.SourceView) (store.SourceUpdate, error) {
		return store.SourceUpdate{AutoCheckpoint: &enabled, NewRevisions: []string{id}}, nil
	}))
	stack, err := s.Get(sourceTestName)
	sourceTestOK(t, err)
	if !stack.AutoCheckpoint || len(stack.SourceHistory.Revisions) != 1 {
		t.Fatal("missing preference or baseline")
	}
	latest := sourceTestReview(t, s)
	enabled = false
	sourceTestOK(t, s.CommitSource(ctx, sourceTestName, latest.Baseline, func(store.SourceView) (store.SourceUpdate, error) {
		return store.SourceUpdate{AutoCheckpoint: &enabled}, nil
	}))
	stack, err = s.Get(sourceTestName)
	sourceTestOK(t, err)
	if stack.AutoCheckpoint || !slices.Equal(stack.SourceHistory.Revisions, []string{id}) || string(sourceTestRead(t, filepath.Join(dir, "compose.pkl"))) != sourceTestOriginal {
		t.Fatal("disabling modified lineage or source")
	}
	sourceTestAbsent(t, stage)
}

func TestFSStoreSourceRestoreReplacesTreeAndAppendsHistory(t *testing.T) {
	ctx := context.Background()
	s, dir, stage := sourceTestWorkspace(t)
	const nested = "modules/shared/values.pkl"
	sourceTestWrite(t, dir, "release.pkl", "release = true\n")
	sourceTestWrite(t, dir, nested, sourceTestOriginal)
	sourceTestOK(t, s.SetEntrypoint(sourceTestName, "release.pkl"))
	captureID, rescueID, restoreID := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	snapshot := sourceTestReview(t, s)
	if snapshot.Entrypoint != "release.pkl" || string(snapshot.Files[nested]) != sourceTestOriginal {
		t.Fatalf("snapshot omitted entrypoint or nested source: %+v", snapshot)
	}
	sourceTestOK(t, s.CommitSource(ctx, sourceTestName, snapshot.Baseline, func(store.SourceView) (store.SourceUpdate, error) {
		return store.SourceUpdate{NewRevisions: []string{captureID}}, nil
	}))
	sourceTestWrite(t, dir, nested, sourceTestEdited)
	sourceTestWrite(t, dir, "draft.pkl", "draft = true\n")
	sourceTestWrite(t, dir, "draft/only.pkl", "draftOnly = true\n")
	sourceTestOK(t, s.SetEntrypoint(sourceTestName, "draft.pkl"))
	live := sourceTestReview(t, s)
	metaPath := filepath.Join(dir, "meta.json")
	liveMeta := sourceTestRead(t, metaPath)

	// Pin the old directory inode so identity cannot be confused by inode reuse.
	oldDir, err := os.Open(dir)
	sourceTestOK(t, err)
	defer oldDir.Close()
	oldInfo, err := oldDir.Stat()
	sourceTestOK(t, err)
	sourceTestOK(t, s.CommitSource(ctx, sourceTestName, live.Baseline, func(v store.SourceView) (store.SourceUpdate, error) {
		if v.Scope != live.Scope || v.Entrypoint != "draft.pkl" || !slices.Equal(v.Revisions, []string{captureID}) ||
			!maps.EqualFunc(v.Files, live.Files, bytes.Equal) {
			t.Fatalf("restore callback view = %+v, live review = %+v", v, live)
		}
		if !bytes.Equal(sourceTestRead(t, metaPath), liveMeta) || string(sourceTestRead(t, filepath.Join(dir, nested))) != sourceTestEdited {
			t.Fatal("restore modified the workspace before the callback returned")
		}
		sourceTestAbsent(t, stage)
		return store.SourceUpdate{
			NewRevisions: []string{rescueID, restoreID},
			Entrypoint:   snapshot.Entrypoint,
			Files:        snapshot.Files,
		}, nil
	}))
	newInfo, err := os.Stat(dir)
	sourceTestOK(t, err)
	if os.SameFile(oldInfo, newInfo) {
		t.Fatal("restore edited the workspace in place instead of replacing the directory")
	}
	restored := sourceTestReview(t, s)
	if restored.Baseline == live.Baseline || restored.Scope != live.Scope || restored.Scope == "" ||
		!slices.Equal(restored.Revisions, []string{captureID, rescueID, restoreID}) ||
		!maps.EqualFunc(restored.Files, snapshot.Files, bytes.Equal) {
		t.Fatalf("restored view = %+v, source snapshot = %+v", restored, snapshot)
	}
	stack, err := s.Get(sourceTestName)
	sourceTestOK(t, err)
	if want := filepath.Join(dir, snapshot.Entrypoint); stack.PklPath != want {
		t.Fatalf("restored entrypoint path = %q, want %q", stack.PklPath, want)
	}
	sourceTestAbsent(t, filepath.Join(dir, "draft.pkl"))
	sourceTestAbsent(t, filepath.Join(dir, "draft"))
	sourceTestAbsent(t, stage)
}

func TestFSStoreSourcePrePublicationFailure(t *testing.T) {
	ctx := context.Background()
	s, dir, stage := sourceTestWorkspace(t)
	before := sourceTestReview(t, s)
	metaPath := filepath.Join(dir, "meta.json")
	meta := sourceTestRead(t, metaPath)
	archiveErr := errors.New("synthetic archive write failure")
	for _, tc := range []struct {
		name     string
		entry    string
		buildErr error
		wantErr  error
	}{
		{"callback error", "release.pkl", archiveErr, archiveErr},
		{"missing entrypoint", "missing.pkl", nil, store.ErrSourceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := s.CommitSource(ctx, sourceTestName, before.Baseline, func(store.SourceView) (store.SourceUpdate, error) {
				return store.SourceUpdate{
					NewRevisions: []string{strings.Repeat("a", 64)},
					Entrypoint:   tc.entry,
					Files:        map[string][]byte{"release.pkl": []byte(sourceTestEdited)},
				}, tc.buildErr
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("CommitSource = %v, want %v", err, tc.wantErr)
			}
			after := sourceTestReview(t, s)
			if after.Baseline != before.Baseline || after.Scope != "" || len(after.Revisions) != 0 ||
				!bytes.Equal(sourceTestRead(t, metaPath), meta) {
				t.Fatalf("failed commit changed workspace: before = %+v, after = %+v", before, after)
			}
			sourceTestAbsent(t, filepath.Join(dir, "release.pkl"))
			sourceTestAbsent(t, stage)
		})
	}
}

func TestFSStoreSourceRejectsExtraFilesAndUnsafeLinks(t *testing.T) {
	for _, kind := range []string{"extra file", "file symlink", "directory symlink", "hard link"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			s, dir, stage := sourceTestWorkspace(t)
			before := sourceTestReview(t, s)
			external := t.TempDir()
			sourceTestWrite(t, external, "target.pkl", sourceTestOriginal)
			target := filepath.Join(external, "target.pkl")
			var unexpected string
			switch kind {
			case "extra file":
				unexpected = filepath.Join(dir, "notes.txt")
				sourceTestWrite(t, dir, "notes.txt", "keep me\n")
			case "file symlink":
				unexpected = filepath.Join(dir, "linked.pkl")
				sourceTestOK(t, os.Symlink(target, unexpected))
			case "directory symlink":
				unexpected = filepath.Join(dir, "linked")
				sourceTestOK(t, os.Symlink(external, unexpected))
			case "hard link":
				unexpected = filepath.Join(dir, "linked.pkl")
				sourceTestOK(t, os.Link(target, unexpected))
			}
			if _, err := s.ReviewSource(ctx, sourceTestName); !errors.Is(err, store.ErrSourceUnavailable) {
				t.Fatalf("ReviewSource = %v, want ErrSourceUnavailable", err)
			}
			err := s.CommitSource(ctx, sourceTestName, before.Baseline, func(store.SourceView) (store.SourceUpdate, error) {
				t.Fatal("commit invoked the callback with an unsafe workspace")
				return store.SourceUpdate{}, nil
			})
			if !errors.Is(err, store.ErrSourceUnavailable) {
				t.Fatalf("CommitSource = %v, want ErrSourceUnavailable", err)
			}
			_, err = os.Lstat(unexpected)
			sourceTestOK(t, err)
			if got := string(sourceTestRead(t, target)); got != sourceTestOriginal {
				t.Fatalf("external target changed: %q", got)
			}
			sourceTestAbsent(t, stage)
		})
	}
}

func TestFSStoreSourceInterruptedFenceBlocksAccess(t *testing.T) {
	ctx := context.Background()
	s, dir, stage := sourceTestWorkspace(t)
	before := sourceTestReview(t, s)
	metaPath := filepath.Join(dir, "meta.json")
	meta := sourceTestRead(t, metaPath)
	// Simulate a surviving staging directory, then reopen the store.
	sourceTestWrite(t, stage, "compose.pkl", sourceTestEdited)
	s, err := store.NewFSStore(filepath.Dir(filepath.Dir(dir)))
	sourceTestOK(t, err)
	if _, err := s.ReviewSource(ctx, sourceTestName); !errors.Is(err, store.ErrSourceInterrupted) {
		t.Fatalf("ReviewSource = %v, want ErrSourceInterrupted", err)
	}
	err = s.CommitSource(ctx, sourceTestName, before.Baseline, func(store.SourceView) (store.SourceUpdate, error) {
		t.Fatal("fenced commit invoked the callback")
		return store.SourceUpdate{}, nil
	})
	if !errors.Is(err, store.ErrSourceInterrupted) {
		t.Fatalf("CommitSource = %v, want ErrSourceInterrupted", err)
	}
	if _, err := s.Get(sourceTestName); !errors.Is(err, store.ErrSourceInterrupted) {
		t.Fatalf("Get = %v, want ErrSourceInterrupted", err)
	}
	if _, err := s.Update(sourceTestName, "value = 3\n"); !errors.Is(err, store.ErrSourceInterrupted) {
		t.Fatalf("Update = %v, want ErrSourceInterrupted", err)
	}
	// Delete reports a refused registration lookup without exposing its cause.
	if err := s.Delete(sourceTestName); err == nil {
		t.Fatal("Delete succeeded despite the interrupted publication fence")
	}
	if !bytes.Equal(sourceTestRead(t, metaPath), meta) ||
		string(sourceTestRead(t, filepath.Join(dir, "compose.pkl"))) != sourceTestOriginal ||
		string(sourceTestRead(t, filepath.Join(stage, "compose.pkl"))) != sourceTestEdited {
		t.Fatal("fenced operations changed the live or staged tree")
	}
}

const (
	sourceTestLedger  = "manual-deployments.json"
	sourceTestSidecar = "manual-deployments.lock"
	sourceTestAnchor  = "manual-deployments.initialized"
)

func sourceTestIntent(stack, requestDigit string) store.ManualDeploymentIntent {
	return store.ManualDeploymentIntent{
		Host: "target", Target: strings.Repeat("a", 64), Stack: stack, Mode: "remove",
		Request: strings.Repeat(requestDigit, 32), PriorRequest: strings.Repeat("c", 32),
		Journal: strings.Repeat("d", 32), Selection: "sha256:" + strings.Repeat("e", 64),
	}
}

func TestFSStoreSourceCommitExcludesConcurrentManualIntent(t *testing.T) {
	s, dir, stage := sourceTestWorkspace(t)
	root := filepath.Dir(filepath.Dir(dir))
	other, err := store.NewFSStore(root)
	sourceTestOK(t, err)
	before := sourceTestReview(t, s)
	if rows, err := other.ExistingManualIntents(); err != nil || len(rows) != 0 {
		t.Fatalf("initial manual history = %v, %v; want empty", rows, err)
	}
	intent := sourceTestIntent(sourceTestName, "b")
	sourceTestOK(t, s.CommitSource(context.Background(), sourceTestName, before.Baseline, func(v store.SourceView) (store.SourceUpdate, error) {
		// This store has a different mutex. An attempt after the ledger was
		// read but before publication must contend on the root inode itself.
		done := make(chan error, 1)
		go func() { done <- other.SaveManualIntent(intent) }()
		select {
		case err := <-done:
			if !errors.Is(err, store.ErrManualIntent) {
				t.Fatalf("concurrent SaveManualIntent = %v, want ErrManualIntent", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("SaveManualIntent did not fail fast under root lock contention")
		}
		return store.SourceUpdate{
			NewRevisions: []string{strings.Repeat("a", 64)},
			Entrypoint:   v.Entrypoint,
			Files:        map[string][]byte{v.Entrypoint: []byte(sourceTestEdited)},
		}, nil
	}))
	for _, name := range []string{sourceTestLedger, sourceTestSidecar, sourceTestAnchor} {
		sourceTestAbsent(t, filepath.Join(root, name))
	}
	sourceTestAbsent(t, stage)
	if got := string(sourceTestReview(t, s).Files["compose.pkl"]); got != sourceTestEdited {
		t.Fatalf("published source = %q, want %q", got, sourceTestEdited)
	}
	sourceTestOK(t, other.SaveManualIntent(intent)) // Now the root lock is free.
}

func sourceTestRejectCommit(t *testing.T, s *store.FSStore, before store.SourceView) {
	t.Helper()
	err := s.CommitSource(context.Background(), sourceTestName, before.Baseline, func(store.SourceView) (store.SourceUpdate, error) {
		t.Fatal("commit invoked the callback despite denied ledger admission")
		return store.SourceUpdate{}, nil
	})
	if !errors.Is(err, store.ErrSourceUnavailable) {
		t.Fatalf("CommitSource = %v, want ErrSourceUnavailable", err)
	}
	if after := sourceTestReview(t, s); after.Baseline != before.Baseline {
		t.Fatal("denied commit changed the workspace")
	}
}

func TestFSStoreSourceCommitRechecksManualIntentsAfterReview(t *testing.T) {
	s, dir, stage := sourceTestWorkspace(t)
	other, err := store.NewFSStore(filepath.Dir(filepath.Dir(dir)))
	sourceTestOK(t, err)
	before := sourceTestReview(t, s)
	if rows, err := s.ExistingManualIntents(); err != nil || len(rows) != 0 {
		t.Fatalf("initial manual history = %v, %v; want empty", rows, err)
	}
	intent := sourceTestIntent(sourceTestName, "b")
	sourceTestOK(t, other.SaveManualIntent(intent))
	sourceTestOK(t, other.SaveManualIntent(sourceTestIntent("other-stack", "f")))
	sourceTestRejectCommit(t, s, before)
	sourceTestAbsent(t, stage)

	// The resolved row for this stack and the unrelated active row are allowed.
	sourceTestOK(t, other.ResolveManualIntent(intent))
	sourceTestOK(t, s.CommitSource(context.Background(), sourceTestName, before.Baseline, func(store.SourceView) (store.SourceUpdate, error) {
		return store.SourceUpdate{NewRevisions: []string{strings.Repeat("a", 64)}}, nil
	}))
}

func TestFSStoreSourceCommitRejectsMalformedManualLedgerWithoutRepair(t *testing.T) {
	intent := sourceTestIntent("other-stack", "b")
	duplicate, err := json.Marshal([]store.ManualDeploymentIntent{intent, intent})
	sourceTestOK(t, err)
	for _, tc := range []struct {
		name, raw string
		missing   []string
		symlink   bool
	}{
		{name: "null", raw: "null"},
		{name: "trailing data", raw: "[] []"},
		{name: "omitted fields", raw: "[{}]"},
		{name: "duplicate request", raw: string(duplicate)},
		{name: "over byte limit", raw: "[]" + strings.Repeat(" ", 4<<20)},
		{name: "symlink ledger", symlink: true},
		{name: "missing anchor", missing: []string{sourceTestAnchor}},
		{name: "missing sidecar", missing: []string{sourceTestSidecar}},
		{name: "only anchor remains", missing: []string{sourceTestLedger, sourceTestSidecar}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, dir, stage := sourceTestWorkspace(t)
			root := filepath.Dir(filepath.Dir(dir))
			ledger := filepath.Join(root, sourceTestLedger)
			before := sourceTestReview(t, s)
			_, err := s.ListManualIntents() // Provision a complete ledger first.
			sourceTestOK(t, err)
			if tc.raw != "" {
				sourceTestOK(t, os.WriteFile(ledger, []byte(tc.raw), 0600))
			}
			if tc.symlink {
				sourceTestWrite(t, root, "other.json", "[]")
				sourceTestOK(t, os.Remove(ledger))
				sourceTestOK(t, os.Symlink("other.json", ledger))
			}
			for _, name := range tc.missing {
				sourceTestOK(t, os.Remove(filepath.Join(root, name)))
			}

			if _, err := s.ExistingManualIntents(); !errors.Is(err, store.ErrManualIntent) {
				t.Fatalf("ExistingManualIntents = %v, want ErrManualIntent", err)
			}
			sourceTestRejectCommit(t, s, before)
			sourceTestAbsent(t, stage)
			for _, name := range tc.missing {
				sourceTestAbsent(t, filepath.Join(root, name))
			}
			if tc.raw != "" && string(sourceTestRead(t, ledger)) != tc.raw {
				t.Fatal("denial rewrote the malformed ledger")
			}
			if tc.symlink {
				link, err := os.Readlink(ledger)
				if err != nil || link != "other.json" {
					t.Fatalf("denial changed the ledger symlink: %q, %v", link, err)
				}
			}
		})
	}
}
