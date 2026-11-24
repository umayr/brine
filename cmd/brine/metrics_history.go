package main

import (
	"flag"

	"github.com/umayr/brine/internal/config"
)

func registerMetricsHistoryFlags(flags *flag.FlagSet, cfg *config.Config) {
	flags.BoolVar(&cfg.MetricsHistoryEnabled, "metrics-history-enabled", false, "request optional metrics history at startup (not yet available)")
	flags.StringVar(&cfg.MetricsHistoryDir, "metrics-history-dir", "", "pre-provisioned private metrics history directory")
	flags.IntVar(&cfg.MetricsHistoryMaxPages, "metrics-history-max-pages", 0, "explicit SQLite main-file page limit, not a filesystem quota")
}
