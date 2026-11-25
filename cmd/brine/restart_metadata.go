package main

import (
	"flag"
	"fmt"

	"github.com/umayr/brine/internal/config"
)

func registerRestartMetadataFlags(flags *flag.FlagSet, cfg *config.Config) {
	s := &cfg.RestartMetadata
	flags.BoolVar(&s.Enabled, "restart-metadata-enabled", false, "request metadata-only archive discovery at startup (not yet available)")
	flags.StringVar(&s.ArchiveDir, "restart-metadata-dir", "", "preprovisioned encrypted archive directory; never initialized automatically")
	flags.StringVar(&s.KeyFile, "restart-metadata-key-file", "", "preprovisioned private archive key file")
	flags.StringVar(&s.StoreID, "restart-metadata-store-id", "", "explicit 64-character lowercase hex archive identity")
	flags.Int64Var(&s.MaxBytes, "restart-metadata-max-bytes", 0, "explicit maximum sealed archive bytes")
	flags.IntVar(&s.MaxOperations, "restart-metadata-max-operations", 0, "explicit maximum archive operations (1..4095)")
	flags.Func("restart-metadata-enrollment", "metadata approval host=32-character lowercase hex incarnation (repeat, maximum 64)", func(v string) error {
		if len(s.Enrollments) >= 64 || len(v) > 161 {
			return fmt.Errorf("restart metadata enrollment limit exceeded")
		}
		s.Enrollments = append(s.Enrollments, v)
		return nil
	})
}
