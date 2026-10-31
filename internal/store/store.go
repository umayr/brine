package store

import "time"

// GitSource describes a remote Git repository that backs a stack.
type GitSource struct {
	URL          string     `json:"url"`                    // remote clone URL
	Branch       string     `json:"branch"`                 // branch to track, default "main"
	Folder       string     `json:"folder"`                 // subfolder within repo, e.g. "stacks/web"
	ReadOnly     bool       `json:"read_only"`              // disables editor and push; brine is deploy-only
	AutoDeploy   bool       `json:"auto_deploy"`            // pull + dill up automatically on new commits
	PollInterval string     `json:"poll_interval"`          // e.g. "5m"; only used when AutoDeploy=true
	LastSyncAt   *time.Time `json:"last_sync_at,omitempty"` // set by syncer after each pull
	LastCommit   string     `json:"last_commit,omitempty"`  // short SHA of last synced commit
	SyncError    string     `json:"sync_error,omitempty"`   // last sync error message, if any
}

// Stack represents a dill stack managed by brine.
type Stack struct {
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	PklPath    string     `json:"pkl_path"`      // absolute path to entrypoint file
	Entrypoint string     `json:"entrypoint"`    // filename within files dir, default "compose.pkl"
	Git        *GitSource `json:"git,omitempty"` // nil for locally-managed stacks
}

// StackStore manages stack metadata and .pkl file storage.
type StackStore interface {
	// List returns all registered stacks.
	List() ([]*Stack, error)

	// Get returns a single stack by name.
	Get(name string) (*Stack, error)

	// Create registers a new stack and writes its .pkl content to disk.
	Create(name, pklContent string) (*Stack, error)

	// Update rewrites the entrypoint .pkl content for an existing stack.
	Update(name, pklContent string) (*Stack, error)

	// Delete removes the stack and all its files from disk.
	Delete(name string) error

	// ReadPkl returns the current entrypoint .pkl file content for a stack.
	ReadPkl(name string) (string, error)

	// Multi-file methods

	// Files returns all .pkl filenames in the stack directory.
	Files(name string) ([]string, error)

	// ReadFile returns the content of a specific file within the stack.
	ReadFile(name, filename string) (string, error)

	// WriteFile writes (or creates) a specific file within the stack.
	WriteFile(name, filename, content string) error

	// DeleteFile removes a file from the stack. Returns an error if it is the entrypoint.
	DeleteFile(name, filename string) error

	// SetEntrypoint changes the entrypoint file for the stack.
	SetEntrypoint(name, filename string) error

	// Git-backed stack methods

	// CreateFromGit registers a new stack whose files live in a cloned Git repo.
	// The repo must already be cloned at the path returned by RepoDir(name).
	// entrypoint is the filename within {repoDir}/{src.Folder} to use as the pkl entrypoint.
	CreateFromGit(name string, src *GitSource, entrypoint string) (*Stack, error)

	// UpdateGitStatus records the result of the most recent git pull/sync.
	UpdateGitStatus(name, commit, syncErr string, syncTime time.Time) error

	// UpdateGitSource updates the mutable git configuration fields (branch, folder,
	// auto_deploy, poll_interval). Does not affect sync status fields.
	UpdateGitSource(name string, src *GitSource) error

	// RepoDir returns the absolute path of the git clone directory for a stack.
	// Returns the computed path even before the stack is registered.
	RepoDir(name string) (string, error)
}

// Recipe represents a reusable Pkl fragment stored in the recipes library.
type Recipe struct {
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Category    string    `json:"category"`
	Description string    `json:"description"`
	Body        string    `json:"body,omitempty"` // populated by Get(); omitted in List()
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// RecipeStore manages the recipe library.
type RecipeStore interface {
	// List returns all recipes.
	List() ([]*Recipe, error)

	// ListByCategory returns recipes grouped by category.
	ListByCategory() (map[string][]*Recipe, error)

	// Get returns a single recipe by slug, including its body.
	Get(slug string) (*Recipe, error)

	// Create adds a new recipe.
	Create(name, category, description, body string) (*Recipe, error)

	// Update rewrites the body of an existing recipe.
	Update(slug, body string) (*Recipe, error)

	// UpdateMeta changes the metadata fields of an existing recipe.
	UpdateMeta(slug, name, category, description string) (*Recipe, error)

	// Delete removes a recipe.
	Delete(slug string) error
}
