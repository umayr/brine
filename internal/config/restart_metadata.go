package config

import (
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"

	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/targetregistry"
)

var ErrRestartMetadataSettings = errors.New("invalid restart metadata startup settings")

// RestartMetadataSettings never initializes an archive or provisions a key.
// Enrollments are host=incarnation pins, not host names to auto-approve.
type RestartMetadataSettings struct {
	Enabled       bool
	ArchiveDir    string
	KeyFile       string
	StoreID       string
	MaxBytes      int64
	MaxOperations int
	Enrollments   []string
}

type RestartMetadataPin struct {
	Host        string
	Incarnation targetregistry.HistoryIncarnation
}

// RestartMetadataStartup performs syntax validation only, with no filesystem
// or registry I/O. The opener must still validate ancestry, aliases, ownership,
// identity and authenticated inventory before exposing a handle.
func (c *Config) RestartMetadataStartup() (snapshothistory.Options, []RestartMetadataPin, error) {
	var empty snapshothistory.Options
	if c == nil {
		return empty, nil, ErrRestartMetadataSettings
	}
	s := c.RestartMetadata
	if !s.Enabled {
		if s.ArchiveDir != "" || s.KeyFile != "" || s.StoreID != "" || s.MaxBytes != 0 || s.MaxOperations != 0 || len(s.Enrollments) != 0 {
			return empty, nil, ErrRestartMetadataSettings
		}
		return empty, nil, nil
	}
	validPath := func(p string) bool {
		return p != "/" && filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n")
	}
	if !c.RemoteEnabled || c.NoAuth || !validPath(c.DataDir) || !validPath(c.RemoteRegistryDir) || !validPath(s.ArchiveDir) || !validPath(s.KeyFile) || s.MaxBytes <= 0 || s.MaxOperations <= 0 || s.MaxOperations >= 4096 || len(s.Enrollments) == 0 || len(s.Enrollments) > 64 {
		return empty, nil, ErrRestartMetadataSettings
	}
	// Reject obvious overlaps now. Filesystem alias checks remain mandatory at open.
	for _, p := range []string{c.DataDir, c.RemoteRegistryDir, c.RemoteForwardDir, c.RemoteRevealAuditDir, c.RemoteRevealSeedPath, c.CoordinationStateDir, c.OperationStateDir, c.StagingSocket, c.MetricsHistoryDir} {
		if p == "" {
			continue
		}
		if !validPath(p) || pathWithin(p, s.ArchiveDir) || pathWithin(s.ArchiveDir, p) || pathWithin(p, s.KeyFile) || pathWithin(s.KeyFile, p) {
			return empty, nil, ErrRestartMetadataSettings
		}
	}
	if pathWithin(s.ArchiveDir, s.KeyFile) || pathWithin(s.KeyFile, s.ArchiveDir) {
		return empty, nil, ErrRestartMetadataSettings
	}
	decode := func(raw string, dst []byte) bool {
		if len(raw) != hex.EncodedLen(len(dst)) || strings.ToLower(raw) != raw {
			return false
		}
		b, err := hex.DecodeString(raw)
		if err != nil {
			return false
		}
		nonzero := false
		for _, v := range b {
			nonzero = nonzero || v != 0
		}
		copy(dst, b)
		return nonzero
	}
	options := snapshothistory.Options{ArchiveDir: s.ArchiveDir, KeyFile: s.KeyFile, WorkspaceDir: c.DataDir, MaxBytes: s.MaxBytes, MaxOperations: s.MaxOperations}
	if !decode(s.StoreID, options.StoreID[:]) {
		return empty, nil, ErrRestartMetadataSettings
	}
	pins := make([]RestartMetadataPin, 0, len(s.Enrollments))
	seen := make(map[string]bool)
	for _, raw := range s.Enrollments {
		host, id, ok := strings.Cut(raw, "=")
		pin := RestartMetadataPin{Host: host}
		if !ok || host == "" || len(host) > 128 || strings.ContainsAny(host, " \t\r\n\x00/\\") || seen[host] || !decode(id, pin.Incarnation[:]) {
			return empty, nil, ErrRestartMetadataSettings
		}
		seen[host] = true
		pins = append(pins, pin)
	}
	return options, pins, nil
}
