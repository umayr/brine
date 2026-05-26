package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FSStore is a filesystem-backed StackStore.
// Layout: {dataDir}/stacks/{name}/compose.pkl  (default entrypoint)
//
//	{dataDir}/stacks/{name}/meta.json
type FSStore struct {
	dataDir string
}

// NewFSStore creates an FSStore rooted at dataDir, creating the stacks
// subdirectory if it does not already exist.
func NewFSStore(dataDir string) (*FSStore, error) {
	stacksDir := filepath.Join(dataDir, "stacks")
	if err := os.MkdirAll(stacksDir, 0755); err != nil {
		return nil, fmt.Errorf("create stacks dir: %w", err)
	}
	return &FSStore{dataDir: dataDir}, nil
}

func (s *FSStore) stackDir(name string) string {
	return filepath.Join(s.dataDir, "stacks", name)
}

// repoDir is where the git clone lives for a git-backed stack.
func (s *FSStore) repoDir(name string) string {
	return filepath.Join(s.stackDir(name), "repo")
}

// filesDir returns the directory that holds .pkl files for a stack.
// For local stacks this is stackDir; for git-backed stacks it is repo/{folder}.
func (s *FSStore) filesDir(name string, git *GitSource) string {
	if git == nil {
		return s.stackDir(name)
	}
	return filepath.Join(s.repoDir(name), git.Folder)
}

func (s *FSStore) pklPath(name string) string {
	return filepath.Join(s.stackDir(name), "compose.pkl")
}

func (s *FSStore) metaPath(name string) string {
	return filepath.Join(s.stackDir(name), "meta.json")
}

// entrypointOrDefault returns ep if non-empty, otherwise "compose.pkl".
func entrypointOrDefault(ep string) string {
	if ep == "" {
		return "compose.pkl"
	}
	return ep
}

func (s *FSStore) readMeta(name string) (*Stack, error) {
	data, err := os.ReadFile(s.metaPath(name))
	if err != nil {
		return nil, err
	}
	var stack Stack
	if err := json.Unmarshal(data, &stack); err != nil {
		return nil, fmt.Errorf("parse meta: %w", err)
	}
	return &stack, nil
}

func (s *FSStore) writeMeta(stack *Stack) error {
	data, err := json.MarshalIndent(stack, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.metaPath(stack.Name), data, 0644)
}

func (s *FSStore) List() ([]*Stack, error) {
	entries, err := os.ReadDir(filepath.Join(s.dataDir, "stacks"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read stacks dir: %w", err)
	}
	stacks := make([]*Stack, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		stack, err := s.readMeta(entry.Name())
		if err != nil {
			continue // skip corrupt entries
		}
		stacks = append(stacks, stack)
	}
	return stacks, nil
}

func (s *FSStore) Get(name string) (*Stack, error) {
	stack, err := s.readMeta(name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("stack %q not found", name)
		}
		return nil, err
	}
	return stack, nil
}

func (s *FSStore) Create(name, eng, pklContent string) (*Stack, error) {
	dir := s.stackDir(name)
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("stack %q already exists", name)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create stack dir: %w", err)
	}
	if err := os.WriteFile(s.pklPath(name), []byte(pklContent), 0644); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("write pkl: %w", err)
	}
	now := time.Now()
	stack := &Stack{
		Name:       name,
		Engine:     eng,
		CreatedAt:  now,
		UpdatedAt:  now,
		PklPath:    s.pklPath(name),
		Entrypoint: "compose.pkl",
	}
	if err := s.writeMeta(stack); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("write meta: %w", err)
	}
	return stack, nil
}

func (s *FSStore) Update(name, pklContent string) (*Stack, error) {
	stack, err := s.readMeta(name)
	if err != nil {
		return nil, fmt.Errorf("stack %q not found", name)
	}
	if stack.Git != nil && stack.Git.ReadOnly {
		return nil, fmt.Errorf("stack %q is read-only (git-backed)", name)
	}
	ep := entrypointOrDefault(stack.Entrypoint)
	epPath := filepath.Join(s.filesDir(name, stack.Git), ep)
	if err := os.WriteFile(epPath, []byte(pklContent), 0644); err != nil {
		return nil, fmt.Errorf("write pkl: %w", err)
	}
	stack.UpdatedAt = time.Now()
	if err := s.writeMeta(stack); err != nil {
		return nil, fmt.Errorf("write meta: %w", err)
	}
	return stack, nil
}

func (s *FSStore) Delete(name string) error {
	if _, err := s.readMeta(name); err != nil {
		return fmt.Errorf("stack %q not found", name)
	}
	return os.RemoveAll(s.stackDir(name))
}

func (s *FSStore) ReadPkl(name string) (string, error) {
	stack, err := s.readMeta(name)
	if err != nil {
		return "", fmt.Errorf("stack %q not found", name)
	}
	ep := entrypointOrDefault(stack.Entrypoint)
	data, err := os.ReadFile(filepath.Join(s.filesDir(name, stack.Git), ep))
	if err != nil {
		return "", fmt.Errorf("read pkl for %q: %w", name, err)
	}
	return string(data), nil
}

// Files returns all .pkl filenames within the stack's files directory.
func (s *FSStore) Files(name string) ([]string, error) {
	stack, err := s.readMeta(name)
	if err != nil {
		return nil, fmt.Errorf("stack %q not found", name)
	}
	dir := s.filesDir(name, stack.Git)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read stack dir: %w", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".pkl") {
			files = append(files, e.Name())
		}
	}
	return files, nil
}

// ReadFile returns the content of a specific .pkl file within a stack.
func (s *FSStore) ReadFile(name, filename string) (string, error) {
	if err := validateFilename(filename); err != nil {
		return "", err
	}
	stack, err := s.readMeta(name)
	if err != nil {
		return "", fmt.Errorf("stack %q not found", name)
	}
	data, err := os.ReadFile(filepath.Join(s.filesDir(name, stack.Git), filename))
	if err != nil {
		return "", fmt.Errorf("read file %q: %w", filename, err)
	}
	return string(data), nil
}

// WriteFile creates or overwrites a .pkl file within a stack.
func (s *FSStore) WriteFile(name, filename, content string) error {
	if err := validateFilename(filename); err != nil {
		return err
	}
	stack, err := s.readMeta(name)
	if err != nil {
		return fmt.Errorf("stack %q not found", name)
	}
	if stack.Git != nil && stack.Git.ReadOnly {
		return fmt.Errorf("stack %q is read-only (git-backed)", name)
	}
	if err := os.WriteFile(filepath.Join(s.filesDir(name, stack.Git), filename), []byte(content), 0644); err != nil {
		return fmt.Errorf("write file %q: %w", filename, err)
	}
	stack.UpdatedAt = time.Now()
	return s.writeMeta(stack)
}

// DeleteFile removes a .pkl file from a stack. Returns an error if it is the entrypoint or read-only.
func (s *FSStore) DeleteFile(name, filename string) error {
	if err := validateFilename(filename); err != nil {
		return err
	}
	stack, err := s.readMeta(name)
	if err != nil {
		return fmt.Errorf("stack %q not found", name)
	}
	if stack.Git != nil && stack.Git.ReadOnly {
		return fmt.Errorf("stack %q is read-only (git-backed)", name)
	}
	if entrypointOrDefault(stack.Entrypoint) == filename {
		return fmt.Errorf("cannot delete the entrypoint file %q", filename)
	}
	return os.Remove(filepath.Join(s.filesDir(name, stack.Git), filename))
}

// SetEntrypoint changes the entrypoint file for a stack.
func (s *FSStore) SetEntrypoint(name, filename string) error {
	if err := validateFilename(filename); err != nil {
		return err
	}
	stack, err := s.readMeta(name)
	if err != nil {
		return fmt.Errorf("stack %q not found", name)
	}
	if stack.Git != nil && stack.Git.ReadOnly {
		return fmt.Errorf("stack %q is read-only (git-backed)", name)
	}
	dir := s.filesDir(name, stack.Git)
	if _, err := os.Stat(filepath.Join(dir, filename)); err != nil {
		return fmt.Errorf("file %q does not exist in stack", filename)
	}
	stack.Entrypoint = filename
	stack.PklPath = filepath.Join(dir, filename)
	stack.UpdatedAt = time.Now()
	return s.writeMeta(stack)
}

// CreateFromGit registers a new git-backed stack. The repo must already
// be cloned at {stackDir}/repo/ by the caller.
func (s *FSStore) CreateFromGit(name, eng string, src *GitSource, entrypoint string) (*Stack, error) {
	// Check meta.json rather than the directory — the repo clone may have
	// already created the parent stack dir via MkdirAll.
	if _, err := os.Stat(s.metaPath(name)); err == nil {
		return nil, fmt.Errorf("stack %q already exists", name)
	}
	if err := os.MkdirAll(s.stackDir(name), 0755); err != nil {
		return nil, fmt.Errorf("create stack dir: %w", err)
	}
	if entrypoint == "" {
		entrypoint = "compose.pkl"
	}
	filesDir := s.filesDir(name, src)
	pklPath := filepath.Join(filesDir, entrypoint)
	// Verify the entrypoint exists in the cloned repo.
	if _, err := os.Stat(pklPath); err != nil {
		_ = os.RemoveAll(s.stackDir(name))
		return nil, fmt.Errorf("entrypoint %q not found in repo (folder: %q)", entrypoint, src.Folder)
	}
	now := time.Now()
	stack := &Stack{
		Name:       name,
		Engine:     eng,
		CreatedAt:  now,
		UpdatedAt:  now,
		PklPath:    pklPath,
		Entrypoint: entrypoint,
		Git:        src,
	}
	if err := s.writeMeta(stack); err != nil {
		_ = os.RemoveAll(s.stackDir(name))
		return nil, fmt.Errorf("write meta: %w", err)
	}
	return stack, nil
}

// UpdateGitStatus records the result of the most recent git pull.
func (s *FSStore) UpdateGitStatus(name, commit, syncErr string, syncTime time.Time) error {
	stack, err := s.readMeta(name)
	if err != nil {
		return fmt.Errorf("stack %q not found", name)
	}
	if stack.Git == nil {
		return fmt.Errorf("stack %q is not git-backed", name)
	}
	stack.Git.LastSyncAt = &syncTime
	stack.Git.LastCommit = commit
	stack.Git.SyncError = syncErr
	return s.writeMeta(stack)
}

// UpdateGitSource updates the mutable configuration fields of a git-backed stack.
func (s *FSStore) UpdateGitSource(name string, src *GitSource) error {
	stack, err := s.readMeta(name)
	if err != nil {
		return fmt.Errorf("stack %q not found", name)
	}
	if stack.Git == nil {
		return fmt.Errorf("stack %q is not git-backed", name)
	}
	// Preserve status fields; only update config fields.
	src.LastSyncAt = stack.Git.LastSyncAt
	src.LastCommit = stack.Git.LastCommit
	src.SyncError = stack.Git.SyncError
	stack.Git = src
	stack.UpdatedAt = time.Now()
	return s.writeMeta(stack)
}

// RepoDir returns the absolute path of the git clone directory for a stack.
// This path is valid even before the stack is registered (e.g. during git clone).
func (s *FSStore) RepoDir(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("stack name is required")
	}
	return s.repoDir(name), nil
}

// validateFilename ensures a filename is a safe .pkl filename with no path separators.
func validateFilename(filename string) error {
	if filename == "" {
		return fmt.Errorf("filename cannot be empty")
	}
	if !strings.HasSuffix(filename, ".pkl") {
		return fmt.Errorf("filename must end in .pkl")
	}
	if strings.ContainsAny(filename, "/\\") {
		return fmt.Errorf("filename must not contain path separators")
	}
	return nil
}
