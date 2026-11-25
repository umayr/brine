package server

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"

	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/remoteui"
	"github.com/umayr/brine/internal/snapshothistory"
)

// validateMetadataPaths refuses aliases to other configured state before Open.
// Archive/key/workspace ancestry and ownership are also checked descriptor-by-
// descriptor by snapshothistory.Open. No path is created or repaired here.
func validateMetadataPaths(cfg *config.Config) error {
	resolved := *cfg
	paths := []*string{&resolved.DataDir, &resolved.RemoteRegistryDir, &resolved.RemoteForwardDir, &resolved.RemoteRevealAuditDir, &resolved.RemoteRevealSeedPath, &resolved.CoordinationStateDir, &resolved.OperationStateDir, &resolved.StagingSocket, &resolved.MetricsHistoryDir, &resolved.RestartMetadata.ArchiveDir, &resolved.RestartMetadata.KeyFile}
	infos := make([]os.FileInfo, len(paths))
	for i, p := range paths {
		if *p == "" {
			continue
		}
		canonical, err := filepath.EvalSymlinks(*p)
		if err != nil {
			return config.ErrRestartMetadataSettings
		}
		info, err := os.Stat(canonical)
		if err != nil {
			return config.ErrRestartMetadataSettings
		}
		*p = canonical
		infos[i] = info
	}
	for i := 0; i < len(infos)-2; i++ {
		for j := len(infos) - 2; j < len(infos); j++ {
			if infos[i] != nil && infos[j] != nil && os.SameFile(infos[i], infos[j]) {
				return config.ErrRestartMetadataSettings
			}
		}
	}
	_, _, err := resolved.RestartMetadataStartup()
	return err
}

func (s *Server) beginRestartMetadata(ctx context.Context) *metadataWorker {
	settings := *s.cfg
	settings.RestartMetadata.Enrollments = append([]string(nil), settings.RestartMetadata.Enrollments...)
	if !settings.RestartMetadata.Enabled {
		if _, _, err := settings.RestartMetadataStartup(); err != nil {
			log.Print("optional restart metadata unavailable")
		}
		return nil
	}
	return beginMetadataWorker(ctx, true, func(ctx context.Context) (metadataEndpoint, error) {
		endpoint, err := s.openRestartMetadata(ctx, &settings)
		if err != nil && ctx.Err() == nil {
			log.Print("optional restart metadata unavailable")
		}
		return endpoint, err
	})
}

func (s *Server) openRestartMetadata(ctx context.Context, cfg *config.Config) (metadataEndpoint, error) {
	var empty metadataEndpoint
	options, pins, err := cfg.RestartMetadataStartup()
	if err != nil || !cfg.RestartMetadata.Enabled || s.authn == nil || !s.authn.Enabled() || s.remoteRegistry == nil || ctx.Err() != nil {
		return empty, config.ErrRestartMetadataSettings
	}
	if err := validateMetadataPaths(cfg); err != nil || ctx.Err() != nil {
		return empty, config.ErrRestartMetadataSettings
	}
	archive, err := snapshothistory.Open(ctx, options) // existing-only; never Initialize
	if err != nil {
		return empty, errors.New("restart metadata unavailable")
	}
	endpoint := metadataEndpoint{close: archive.Close}
	handler, err := remoteui.NewRestartMetadataHandler(ctx, s.authn, s.remoteRegistry, archive, pins)
	if err != nil {
		return endpoint, err
	} // worker owns cleanup even on constructor failure
	endpoint.handler = handler
	return endpoint, nil
}
