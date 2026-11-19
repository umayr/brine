package store_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/umayr/brine/internal/store"
)

func TestLocalNestedModuleEditorRoundTrip(t *testing.T) {
	root := t.TempDir()
	st, err := store.NewFSStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Create("local", "import \"modules/settings.pkl\"\n"); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "stacks", "local")
	dir := filepath.Join(workspace, "modules")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.pkl"), []byte("value = 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	files, err := st.LocalEditorFiles("local")
	if err != nil || !slices.Contains(files, "modules/settings.pkl") {
		t.Fatalf("discovery %v: %v", files, err)
	}
	if err := st.WriteLocalEditorFile("local", "modules/settings.pkl", "value = 2\n"); err != nil {
		t.Fatal(err)
	}
	got, err := st.ReadLocalEditorFile("local", "modules/settings.pkl")
	if err != nil || got != "value = 2\n" {
		t.Fatalf("read: %q %v", got, err)
	}
	for _, p := range []string{"../outside.pkl", "modules/../compose.pkl", "modules//settings.pkl", "/modules/settings.pkl", `modules\settings.pkl`, "modules/missing.pkl", "modules/PklProject.deps.json"} {
		if err := st.WriteLocalEditorFile("local", p, "bad"); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
	if err := os.Symlink(dir, filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "settings.pkl"), filepath.Join(dir, "alias.pkl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir, "settings.pkl"), filepath.Join(dir, "hard.pkl")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"link/settings.pkl", "modules/alias.pkl", "modules/hard.pkl", "modules/settings.pkl"} {
		if _, err := st.ReadLocalEditorFile("local", p); err == nil {
			t.Errorf("accepted unsafe %q", p)
		}
	}
	if err := st.WriteFile("local", "modules/settings.pkl", "bad"); err == nil {
		t.Fatal("root API broadened")
	}
}

func TestLocalEditorRejectsGitWorkspace(t *testing.T) {
	root := t.TempDir()
	st, err := store.NewFSStore(root)
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "stacks", "gitstack", "repo")
	if err := os.MkdirAll(filepath.Join(repo, "modules"), 0700); err != nil {
		t.Fatal(err)
	}
	for p, content := range map[string]string{"compose.pkl": "x", "modules/a.pkl": "x"} {
		if err := os.WriteFile(filepath.Join(repo, p), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateFromGit("gitstack", &store.GitSource{URL: "synthetic", Branch: "main"}, "compose.pkl"); err != nil {
		t.Fatal(err)
	}
	if files, err := st.LocalEditorFiles("gitstack"); err != nil || len(files) != 0 {
		t.Fatalf("git files %v: %v", files, err)
	}
	if _, err := st.ReadLocalEditorFile("gitstack", "modules/a.pkl"); err == nil {
		t.Fatal("git read accepted")
	}
	if err := st.WriteLocalEditorFile("gitstack", "modules/a.pkl", "bad"); err == nil {
		t.Fatal("git write accepted")
	}
}
