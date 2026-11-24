package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var ErrMetricsHistorySettings = errors.New("invalid metrics history startup settings")

// MetricsHistoryStartupDir returns a candidate restart-only launch option.
// It performs no database I/O. Default-off does no path lookup, and errors
// belong to optional history only; ordinary Config.Validate is unchanged.
func (c *Config) MetricsHistoryStartupDir() (string, error) {
	root := c.MetricsHistoryDir
	if !c.MetricsHistoryEnabled {
		if root != "" || c.MetricsHistoryMaxPages != 0 {
			return "", ErrMetricsHistorySettings
		}
		return "", nil
	}
	isAbsolute := func(p string) bool {
		return p != "/" && filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n")
	}
	if c.MetricsHistoryMaxPages < 2 || !c.RemoteEnabled || c.NoAuth || !isAbsolute(root) || !isAbsolute(c.DataDir) || !isAbsolute(c.RemoteRegistryDir) {
		return "", ErrMetricsHistorySettings
	}
	// The store checks trusted ancestry and symlinks of its own root at open.
	// Here reject overlaps with all used app state paths, including aliases.
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() {
		return "", ErrMetricsHistorySettings
	}
	for _, p := range []string{c.DataDir, c.RemoteRegistryDir, c.RemoteForwardDir, c.RemoteRevealAuditDir, c.RemoteRevealSeedPath, c.CoordinationStateDir, c.OperationStateDir, c.StagingSocket} {
		if p == "" {
			continue
		}
		if !isAbsolute(p) || pathWithin(root, p) || pathWithin(p, root) {
			return "", ErrMetricsHistorySettings
		}
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil || pathWithin(root, resolved) || pathWithin(resolved, root) {
			return "", ErrMetricsHistorySettings
		}
		other, err := os.Stat(resolved)
		if err != nil || os.SameFile(rootInfo, other) {
			return "", ErrMetricsHistorySettings
		}
	}
	return root, nil
}

func pathWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
