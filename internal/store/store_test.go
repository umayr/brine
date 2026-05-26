package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/umayr/brine/internal/store"
)

func TestFSStore_MultiFile(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewFSStore(dir)
	if err != nil {
		t.Fatalf("NewFSStore: %v", err)
	}

	// Create a stack.
	s, err := st.Create("mystack", "docker", "amends \"dill.pkl\"\n")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if s.Entrypoint != "compose.pkl" {
		t.Errorf("Entrypoint = %q, want compose.pkl", s.Entrypoint)
	}

	// List files: should have compose.pkl.
	files, err := st.Files("mystack")
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 1 || files[0] != "compose.pkl" {
		t.Errorf("Files = %v, want [compose.pkl]", files)
	}

	// WriteFile: add a new file.
	if err := st.WriteFile("mystack", "shared.pkl", "// shared\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// List files: should have both.
	files, err = st.Files("mystack")
	if err != nil {
		t.Fatalf("Files after WriteFile: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("len(Files) = %d, want 2", len(files))
	}

	// ReadFile.
	content, err := st.ReadFile("mystack", "shared.pkl")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if content != "// shared\n" {
		t.Errorf("ReadFile content = %q, want %q", content, "// shared\n")
	}

	// SetEntrypoint.
	if err := st.SetEntrypoint("mystack", "shared.pkl"); err != nil {
		t.Fatalf("SetEntrypoint: %v", err)
	}
	updated, err := st.Get("mystack")
	if err != nil {
		t.Fatalf("Get after SetEntrypoint: %v", err)
	}
	if updated.Entrypoint != "shared.pkl" {
		t.Errorf("Entrypoint = %q, want shared.pkl", updated.Entrypoint)
	}
	expectedPklPath := filepath.Join(dir, "stacks", "mystack", "shared.pkl")
	if updated.PklPath != expectedPklPath {
		t.Errorf("PklPath = %q, want %q", updated.PklPath, expectedPklPath)
	}

	// DeleteFile: cannot delete the entrypoint.
	if err := st.DeleteFile("mystack", "shared.pkl"); err == nil {
		t.Error("DeleteFile entrypoint should return error")
	}

	// DeleteFile: can delete a non-entrypoint.
	if err := st.DeleteFile("mystack", "compose.pkl"); err != nil {
		t.Fatalf("DeleteFile non-entrypoint: %v", err)
	}
	files, _ = st.Files("mystack")
	if len(files) != 1 || files[0] != "shared.pkl" {
		t.Errorf("Files after DeleteFile = %v, want [shared.pkl]", files)
	}

	// validateFilename: reject files with no .pkl suffix.
	if err := st.WriteFile("mystack", "bad.txt", ""); err == nil {
		t.Error("WriteFile bad.txt should return error")
	}
	// Reject path separators.
	if err := st.WriteFile("mystack", "../evil.pkl", ""); err == nil {
		t.Error("WriteFile path traversal should return error")
	}
}

func TestFSStore_ReadOnly(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewFSStore(dir)
	if err != nil {
		t.Fatalf("NewFSStore: %v", err)
	}
	_, err = st.Create("ro", "docker", "content\n")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Simulate a git-backed read-only stack by writing meta directly via UpdateGitSource.
	// First we need to get the stack and set Git on it; easiest via CreateFromGit with a fake repo.
	// Instead, just verify that WriteFile returns an error when the stack has ReadOnly=true.
	// We'll add a GitSource via UpdateGitSource after CreateFromGit.

	// Create a mock repo dir so CreateFromGit can verify the entrypoint exists.
	repoDir, _ := st.RepoDir("ro-git")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := os.WriteFile(repoDir+"/compose.pkl", []byte("x\n"), 0644); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}

	src := &store.GitSource{URL: "https://example.com/repo.git", Branch: "main", ReadOnly: true}
	if _, err := st.CreateFromGit("ro-git", "docker", src, "compose.pkl"); err != nil {
		t.Fatalf("CreateFromGit: %v", err)
	}

	// WriteFile on a read-only git stack should fail.
	if err := st.WriteFile("ro-git", "compose.pkl", "new content\n"); err == nil {
		t.Error("WriteFile on read-only stack should return error")
	}

	// Update (entrypoint write) should also fail.
	if _, err := st.Update("ro-git", "new content\n"); err == nil {
		t.Error("Update on read-only stack should return error")
	}

	// DeleteFile should fail.
	_ = st.WriteFile("ro-git", "other.pkl", "") // pre-create via direct OS write to bypass guard
	_ = os.WriteFile(repoDir+"/other.pkl", []byte(""), 0644)
	if err := st.DeleteFile("ro-git", "other.pkl"); err == nil {
		t.Error("DeleteFile on read-only stack should return error")
	}
}

func TestFSStore_ReadPkl_UsesEntrypoint(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewFSStore(dir)
	if err != nil {
		t.Fatalf("NewFSStore: %v", err)
	}
	_, err = st.Create("s", "docker", "original\n")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_ = st.WriteFile("s", "alt.pkl", "alternate\n")
	_ = st.SetEntrypoint("s", "alt.pkl")

	content, err := st.ReadPkl("s")
	if err != nil {
		t.Fatalf("ReadPkl: %v", err)
	}
	if content != "alternate\n" {
		t.Errorf("ReadPkl = %q, want alternate", content)
	}
}

func TestFSRecipeStore(t *testing.T) {
	dir := t.TempDir()
	rs, err := store.NewFSRecipeStore(dir)
	if err != nil {
		t.Fatalf("NewFSRecipeStore: %v", err)
	}

	// Should have seeded built-in recipes.
	all, err := rs.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) == 0 {
		t.Error("expected seeded recipes, got none")
	}

	// Create a new recipe.
	r, err := rs.Create("My Recipe", "Testing", "A test recipe", "// body\n")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if r.Slug != "my-recipe" {
		t.Errorf("Slug = %q, want my-recipe", r.Slug)
	}

	// Get with body.
	got, err := rs.Get("my-recipe")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Body != "// body\n" {
		t.Errorf("Body = %q, want %q", got.Body, "// body\n")
	}
	if got.Category != "Testing" {
		t.Errorf("Category = %q, want Testing", got.Category)
	}

	// Update body.
	updated, err := rs.Update("my-recipe", "// updated\n")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Body != "// updated\n" {
		t.Errorf("Updated body = %q", updated.Body)
	}

	// UpdateMeta.
	_, err = rs.UpdateMeta("my-recipe", "My Renamed Recipe", "Testing2", "Updated desc")
	if err != nil {
		t.Fatalf("UpdateMeta: %v", err)
	}
	got, _ = rs.Get("my-recipe")
	if got.Name != "My Renamed Recipe" || got.Category != "Testing2" {
		t.Errorf("UpdateMeta not applied: %+v", got)
	}

	// ListByCategory.
	byCategory, err := rs.ListByCategory()
	if err != nil {
		t.Fatalf("ListByCategory: %v", err)
	}
	if _, ok := byCategory["Testing2"]; !ok {
		t.Error("expected Testing2 category in ListByCategory")
	}

	// Delete.
	if err := rs.Delete("my-recipe"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := rs.Get("my-recipe"); err == nil {
		t.Error("Get after Delete should return error")
	}

	// Second creation of same name should succeed (slug reuse after delete).
	_, err = rs.Create("My Recipe", "Testing", "", "// new\n")
	if err != nil {
		t.Fatalf("Create after delete: %v", err)
	}

	// Verify body file is written to the recipes dir.
	bodyPath := filepath.Join(dir, "recipes", "my-recipe", "body.pkl")
	if _, err := os.Stat(bodyPath); err != nil {
		t.Errorf("body.pkl not found at expected path: %v", err)
	}
}
