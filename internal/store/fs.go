package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// FSStore is a filesystem-backed StackStore.
// Layout: {dataDir}/stacks/{name}/compose.pkl  (default entrypoint)
//
//	{dataDir}/stacks/{name}/meta.json
type FSStore struct {
	dataDir string
	mu      sync.RWMutex
}

// NewFSStore creates an FSStore rooted at dataDir, creating the stacks
// subdirectory if it does not already exist.
func NewFSStore(dataDir string) (*FSStore, error) {
	if info, err := os.Lstat(dataDir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("unsafe data directory")
	}
	if err := mkdirPrivate(dataDir); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	stacksDir := filepath.Join(dataDir, "stacks")
	if info, err := os.Lstat(stacksDir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("unsafe stacks directory")
	}
	if err := mkdirPrivate(stacksDir); err != nil {
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
	if err := ValidateStackName(name); err != nil {
		return nil, err
	}
	fd, err := s.stackFD(name)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	data, err := readWorkspaceFile(fd, "meta.json")
	if err != nil {
		return nil, err
	}
	var stack Stack
	if err := json.Unmarshal(data, &stack); err != nil {
		return nil, fmt.Errorf("parse meta: %w", err)
	}
	stack.Name = name
	if err := validateFilename(entrypointOrDefault(stack.Entrypoint)); err != nil {
		return nil, fmt.Errorf("stack %q metadata: %w", name, err)
	}
	if stack.Git != nil {
		if err := validateFolder(stack.Git.Folder); err != nil {
			return nil, fmt.Errorf("stack %q metadata: %w", name, err)
		}
	}
	// Recompute the entrypoint path from the current data directory so that a
	// moved data dir (or a containerised brine) does not keep stale absolute paths.
	stack.PklPath = filepath.Join(s.filesDir(name, stack.Git), entrypointOrDefault(stack.Entrypoint))
	return &stack, nil
}

func (s *FSStore) writeMeta(stack *Stack) error {
	data, err := json.MarshalIndent(stack, "", "  ")
	if err != nil {
		return err
	}
	fd, err := s.stackFD(stack.Name)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return writeWorkspaceFile(fd, "meta.json", data)
}

func (s *FSStore) List() ([]*Stack, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
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
		if ValidateStackName(entry.Name()) != nil {
			continue // ignore directories that are not valid stack names
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	stack, err := s.readMeta(name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("stack %q not found", name)
		}
		return nil, err
	}
	return stack, nil
}

func (s *FSStore) Create(name, pklContent string) (*Stack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateStackName(name); err != nil {
		return nil, err
	}
	dir := s.stackDir(name)
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("stack %q already exists", name)
	}
	if err := mkdirPrivate(dir); err != nil {
		return nil, fmt.Errorf("create stack dir: %w", err)
	}
	if err := writeFilePrivate(s.pklPath(name), []byte(pklContent)); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("write pkl: %w", err)
	}
	now := time.Now()
	stack := &Stack{
		Name:       name,
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateStackName(name); err != nil {
		return nil, err
	}
	stack, err := s.readMeta(name)
	if err != nil {
		return nil, fmt.Errorf("stack %q not found", name)
	}
	if stack.Git != nil && stack.Git.ReadOnly {
		return nil, fmt.Errorf("stack %q is read-only (git-backed)", name)
	}
	ep := entrypointOrDefault(stack.Entrypoint)
	fd, err := s.workspaceFD(name, stack.Git)
	if err != nil {
		return nil, fmt.Errorf("unsafe workspace")
	}
	defer unix.Close(fd)
	if err := writeWorkspaceFile(fd, ep, []byte(pklContent)); err != nil {
		return nil, fmt.Errorf("write pkl: %w", err)
	}
	stack.UpdatedAt = time.Now()
	if err := s.writeMeta(stack); err != nil {
		return nil, fmt.Errorf("write meta: %w", err)
	}
	return stack, nil
}

func (s *FSStore) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateStackName(name); err != nil {
		return err
	}
	if _, err := s.readMeta(name); err != nil {
		return fmt.Errorf("stack %q not found", name)
	}
	return os.RemoveAll(s.stackDir(name))
}

func (s *FSStore) ReadPkl(name string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ValidateStackName(name); err != nil {
		return "", err
	}
	stack, err := s.readMeta(name)
	if err != nil {
		return "", fmt.Errorf("stack %q not found", name)
	}
	ep := entrypointOrDefault(stack.Entrypoint)
	fd, err := s.workspaceFD(name, stack.Git)
	if err != nil {
		return "", fmt.Errorf("unsafe workspace")
	}
	defer unix.Close(fd)
	data, err := readWorkspaceFile(fd, ep)
	if err != nil {
		return "", fmt.Errorf("read pkl for %q: %w", name, err)
	}
	return string(data), nil
}

// Files returns all .pkl filenames within the stack's files directory.
func (s *FSStore) Files(name string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ValidateStackName(name); err != nil {
		return nil, err
	}
	stack, err := s.readMeta(name)
	if err != nil {
		return nil, fmt.Errorf("stack %q not found", name)
	}
	fd, err := s.workspaceFD(name, stack.Git)
	if err != nil {
		return nil, fmt.Errorf("unsafe workspace")
	}
	dir := os.NewFile(uintptr(fd), "workspace")
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read stack dir: %w", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".pkl") {
			f, err := openWorkspaceFile(fd, e.Name(), unix.O_RDONLY)
			if err != nil {
				continue
			}
			f.Close()
			files = append(files, e.Name())
		}
	}
	return files, nil
}

// ReadFile returns the content of a specific .pkl file within a stack.
func (s *FSStore) ReadFile(name, filename string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ValidateStackName(name); err != nil {
		return "", err
	}
	if err := validateFilename(filename); err != nil {
		return "", err
	}
	stack, err := s.readMeta(name)
	if err != nil {
		return "", fmt.Errorf("stack %q not found", name)
	}
	fd, err := s.workspaceFD(name, stack.Git)
	if err != nil {
		return "", fmt.Errorf("unsafe workspace")
	}
	defer unix.Close(fd)
	data, err := readWorkspaceFile(fd, filename)
	if err != nil {
		return "", fmt.Errorf("read file %q: %w", filename, err)
	}
	return string(data), nil
}

// WriteFile creates or overwrites a .pkl file within a stack.
func (s *FSStore) WriteFile(name, filename, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateStackName(name); err != nil {
		return err
	}
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
	fd, err := s.workspaceFD(name, stack.Git)
	if err != nil {
		return fmt.Errorf("unsafe workspace")
	}
	defer unix.Close(fd)
	if err := writeWorkspaceFile(fd, filename, []byte(content)); err != nil {
		return fmt.Errorf("write file %q: %w", filename, err)
	}
	stack.UpdatedAt = time.Now()
	return s.writeMeta(stack)
}

// DeleteFile removes a .pkl file from a stack. Returns an error if it is the entrypoint or read-only.
func (s *FSStore) DeleteFile(name, filename string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateStackName(name); err != nil {
		return err
	}
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
	fd, err := s.workspaceFD(name, stack.Git)
	if err != nil {
		return fmt.Errorf("unsafe workspace")
	}
	defer unix.Close(fd)
	f, err := openWorkspaceFile(fd, filename, unix.O_RDONLY)
	if err != nil {
		return err
	}
	f.Close()
	return unix.Unlinkat(fd, filename, 0)
}

// SetEntrypoint changes the entrypoint file for a stack.
func (s *FSStore) SetEntrypoint(name, filename string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateStackName(name); err != nil {
		return err
	}
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
	fd, err := s.workspaceFD(name, stack.Git)
	if err != nil {
		return fmt.Errorf("unsafe workspace")
	}
	defer unix.Close(fd)
	f, err := openWorkspaceFile(fd, filename, unix.O_RDONLY)
	if err != nil {
		return fmt.Errorf("file %q does not exist in stack", filename)
	}
	f.Close()
	stack.Entrypoint = filename
	stack.PklPath = filepath.Join(dir, filename)
	stack.UpdatedAt = time.Now()
	return s.writeMeta(stack)
}

// CreateFromGit registers a new git-backed stack. The repo must already
// be cloned at {stackDir}/repo/ by the caller.
func (s *FSStore) CreateFromGit(name string, src *GitSource, entrypoint string) (*Stack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateStackName(name); err != nil {
		return nil, err
	}
	if src == nil {
		return nil, fmt.Errorf("git source is required")
	}
	if err := validateFolder(src.Folder); err != nil {
		return nil, err
	}
	if entrypoint == "" {
		entrypoint = "compose.pkl"
	}
	if err := validateFilename(entrypoint); err != nil {
		return nil, err
	}
	// Check meta.json rather than the directory — the repo clone may have
	// already created the parent stack dir via MkdirAll.
	if _, err := os.Stat(s.metaPath(name)); err == nil {
		return nil, fmt.Errorf("stack %q already exists", name)
	}
	stackFD, err := s.stackFD(name)
	if err != nil {
		return nil, fmt.Errorf("unsafe stack directory")
	}
	unix.Close(stackFD)
	filesDir := s.filesDir(name, src)
	pklPath := filepath.Join(filesDir, entrypoint)
	// Verify the entrypoint exists in the cloned repo.
	fd, err := s.workspaceFD(name, src)
	if err != nil {
		return nil, fmt.Errorf("unsafe workspace")
	}
	f, err := openWorkspaceFile(fd, entrypoint, unix.O_RDONLY)
	unix.Close(fd)
	if err != nil {
		return nil, fmt.Errorf("unsafe entrypoint")
	}
	f.Close()
	now := time.Now()
	stack := &Stack{
		Name:       name,
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateStackName(name); err != nil {
		return err
	}
	stack, err := s.readMeta(name)
	if err != nil {
		return fmt.Errorf("stack %q not found", name)
	}
	if stack.Git == nil {
		return fmt.Errorf("stack %q is not git-backed", name)
	}
	stack.Git.LastSyncAt = &syncTime
	if syncErr == "" {
		stack.Git.LastCommit = commit
	}
	stack.Git.SyncError = syncErr
	return s.writeMeta(stack)
}

// UpdateGitSource updates the mutable configuration fields of a git-backed stack.
func (s *FSStore) UpdateGitSource(name string, src *GitSource) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateStackName(name); err != nil {
		return err
	}
	if src == nil {
		return fmt.Errorf("git source is required")
	}
	if err := validateFolder(src.Folder); err != nil {
		return err
	}
	stack, err := s.readMeta(name)
	if err != nil {
		return fmt.Errorf("stack %q not found", name)
	}
	if stack.Git == nil {
		return fmt.Errorf("stack %q is not git-backed", name)
	}
	fd, err := s.workspaceFD(name, src)
	if err != nil {
		return fmt.Errorf("unsafe workspace")
	}
	defer unix.Close(fd)
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
	if err := ValidateStackName(name); err != nil {
		return "", err
	}
	fd, err := s.stackFD(name)
	if os.IsNotExist(err) {
		return s.repoDir(name), nil
	}
	if err != nil {
		return "", fmt.Errorf("unsafe stack directory")
	}
	defer unix.Close(fd)
	repo, err := unix.Openat(fd, "repo", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("unsafe repository directory")
	}
	if err == nil {
		unix.Close(repo)
	}
	return s.repoDir(name), nil
}
