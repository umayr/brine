package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	stackNameRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	recipeSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

// ValidateStackName rejects names that could escape the stacks directory or
// collide with reserved path components. Stack names become directory names,
// so this is a security boundary, not just input hygiene.
func ValidateStackName(name string) error {
	if name == "" {
		return errors.New("stack name is required")
	}
	if len(name) > 64 {
		return fmt.Errorf("invalid stack name %q: must be at most 64 characters", name)
	}
	if !stackNameRe.MatchString(name) {
		return fmt.Errorf("invalid stack name %q: use letters, digits, '-' or '_', starting with a letter or digit", name)
	}
	if !filepath.IsLocal(name) {
		return fmt.Errorf("invalid stack name %q: must be a local path component", name)
	}
	return nil
}

// validateFilename ensures a filename is a safe .pkl filename with no path
// separators or traversal components.
func validateFilename(filename string) error {
	if filename == "" {
		return errors.New("filename cannot be empty")
	}
	if !strings.HasSuffix(filename, ".pkl") {
		return fmt.Errorf("filename must end in .pkl")
	}
	if strings.ContainsAny(filename, `/\`) {
		return fmt.Errorf("filename must not contain path separators")
	}
	if filename == "." || filename == ".." || !filepath.IsLocal(filename) {
		return fmt.Errorf("filename must be a local filename")
	}
	if filepath.Base(filename) != filename {
		return fmt.Errorf("filename must not contain a directory component")
	}
	return nil
}

// validateFolder ensures a git subfolder is a relative path that stays inside
// the cloned repository. An empty folder means the repository root.
func validateFolder(folder string) error {
	if folder == "" {
		return nil
	}
	if filepath.IsAbs(folder) || !filepath.IsLocal(folder) {
		return fmt.Errorf("invalid folder %q: must be a relative path inside the repository", folder)
	}
	clean := filepath.Clean(folder)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("invalid folder %q: must not escape the repository", folder)
	}
	return nil
}

func validateRecipeSlug(slug string) error {
	if slug == "" {
		return errors.New("recipe slug is required")
	}
	if !recipeSlugRe.MatchString(slug) {
		return fmt.Errorf("invalid recipe slug %q", slug)
	}
	return nil
}

// writeFilePrivate writes a file with owner-only permissions, tightening the
// mode on pre-existing files as well (os.WriteFile preserves an existing mode).
func writeFilePrivate(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// mkdirPrivate creates a directory tree with owner-only permissions and
// tightens the mode of the leaf directory.
func mkdirPrivate(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}
