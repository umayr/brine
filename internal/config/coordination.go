package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/umayr/brine/internal/target"
)

func (c *Config) QualifiedOperations() bool {
	return c.CoordinationStateDir != "" || c.CoordinationLayout != "" || c.CoordinationDaemonID != ""
}

func (c *Config) validateCoordination() error {
	if !c.QualifiedOperations() {
		return nil
	}
	if c.CoordinationStateDir == "" || c.CoordinationLayout == "" || c.CoordinationDaemonID == "" {
		return fmt.Errorf("qualified operations require all three coordination options")
	}
	if c.Profile != target.Alpha || c.NoAuth || c.Engine != "docker" {
		return fmt.Errorf("qualified operations require authenticated Alpha with explicit Docker target")
	}
	if !filepath.IsAbs(c.DillBin) || filepath.Clean(c.DillBin) != c.DillBin {
		return fmt.Errorf("qualified operations require an explicit absolute operator-selected --dill candidate; bundled v0.7.0 is incompatible")
	}
	if c.CoordinationLayout != "native-local-docker-v1" {
		return fmt.Errorf("unsupported coordination layout")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9:-]{8,128}$`).MatchString(c.CoordinationDaemonID) {
		return fmt.Errorf("invalid independently provisioned expected daemon ID")
	}
	state := c.CoordinationStateDir
	if !filepath.IsAbs(state) || filepath.Clean(state) != state || state == "/" {
		return fmt.Errorf("coordination state must be an absolute canonical preexisting private directory")
	}
	data, err := filepath.Abs(c.DataDir)
	if err != nil {
		return fmt.Errorf("invalid data directory")
	}
	// Resolve existing data aliases before exclusion. A nonexistent data leaf is
	// checked against its nearest existing canonical parent as well.
	canonicalData, err := canonicalProspectivePath(data)
	if err != nil {
		return fmt.Errorf("invalid data directory")
	}
	rel, err := filepath.Rel(canonicalData, state)
	if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return fmt.Errorf("coordination state must be outside Brine data directory")
	}
	if c.SecretTarget != "" || c.SecretPolicy != "" || c.SecretPkl != "" {
		return fmt.Errorf("qualified operations do not support secret mode")
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("qualified operations require native Linux Docker; Desktop, VM/proxy and containerized layouts are unsupported")
	}
	return validateCoordinationState(state)
}

func canonicalProspectivePath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err = canonicalProspectivePath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}
