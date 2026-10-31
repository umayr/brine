package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/umayr/brine/internal/store"
)

func TestValidateStackName(t *testing.T) {
	valid := []string{"a", "mystack", "my-stack_1", "A1", "0abc"}
	for _, name := range valid {
		if err := store.ValidateStackName(name); err != nil {
			t.Errorf("ValidateStackName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"", "..", ".", "../escape", "a/b", `a\b`, "/abs", "./x",
		"-leading", "_leading", "has space", "semi;colon", "dots.are.bad",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 65 chars
	}
	for _, name := range invalid {
		if err := store.ValidateStackName(name); err == nil {
			t.Errorf("ValidateStackName(%q) = nil, want error", name)
		}
	}
}

func TestFSStore_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	st, err := store.NewFSStore(dataDir)
	if err != nil {
		t.Fatalf("NewFSStore: %v", err)
	}

	for _, name := range []string{"../escape", "../../escape", "..", "a/b", "/tmp/escape"} {
		if _, err := st.Create(name, "x\n"); err == nil {
			t.Errorf("Create(%q) = nil error, want rejection", name)
		}
		if _, err := st.RepoDir(name); err == nil {
			t.Errorf("RepoDir(%q) = nil error, want rejection", name)
		}
		if _, err := st.Get(name); err == nil {
			t.Errorf("Get(%q) = nil error, want rejection", name)
		}
		if err := st.Delete(name); err == nil {
			t.Errorf("Delete(%q) = nil error, want rejection", name)
		}
	}

	// Nothing may be written outside data/stacks.
	outside := filepath.Join(root, "escape")
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("traversal created %s outside the data dir", outside)
	}

	// A valid stack must still work.
	if _, err := st.Create("good-stack", "x\n"); err != nil {
		t.Fatalf("Create(valid) failed: %v", err)
	}

	// Git folder and entrypoint are path boundaries too.
	repoDir, _ := st.RepoDir("git-stack")
	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "compose.pkl"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := &store.GitSource{URL: "https://example.com/r.git", Branch: "main", Folder: "../../etc"}
	if _, err := st.CreateFromGit("git-stack", src, "compose.pkl"); err == nil {
		t.Error("CreateFromGit with traversal folder = nil error, want rejection")
	}
	src.Folder = ""
	if _, err := st.CreateFromGit("git-stack", src, "../../../etc/passwd.pkl"); err == nil {
		t.Error("CreateFromGit with traversal entrypoint = nil error, want rejection")
	}
	if _, err := st.CreateFromGit("git-stack", src, "compose.pkl"); err != nil {
		t.Fatalf("CreateFromGit valid: %v", err)
	}
	if _, err := st.CreateFromGit("git-stack", &store.GitSource{URL: "u", Branch: "b", Folder: "/abs"}, "compose.pkl"); err == nil {
		t.Error("CreateFromGit with absolute folder = nil error, want rejection")
	}
}

func TestFSStore_PrivatePermissions(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	st, err := store.NewFSStore(dataDir)
	if err != nil {
		t.Fatalf("NewFSStore: %v", err)
	}
	if _, err := st.Create("priv", "secret = \"value\"\n"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	checks := []struct {
		path string
		want os.FileMode
	}{
		{dataDir, 0o700},
		{filepath.Join(dataDir, "stacks"), 0o700},
		{filepath.Join(dataDir, "stacks", "priv"), 0o700},
		{filepath.Join(dataDir, "stacks", "priv", "compose.pkl"), 0o600},
		{filepath.Join(dataDir, "stacks", "priv", "meta.json"), 0o600},
	}
	for _, c := range checks {
		info, err := os.Stat(c.path)
		if err != nil {
			t.Fatalf("stat %s: %v", c.path, err)
		}
		if got := info.Mode().Perm(); got != c.want {
			t.Errorf("%s mode = %o, want %o", c.path, got, c.want)
		}
	}
}

func TestFSRecipeStore_RejectsTraversal(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	rs, err := store.NewFSRecipeStore(dataDir)
	if err != nil {
		t.Fatalf("NewFSRecipeStore: %v", err)
	}
	for _, slug := range []string{"../escape", "..", "a/b", `a\b`} {
		if _, err := rs.Get(slug); err == nil {
			t.Errorf("Get(%q) = nil error, want rejection", slug)
		}
		if err := rs.Delete(slug); err == nil {
			t.Errorf("Delete(%q) = nil error, want rejection", slug)
		}
	}
}
