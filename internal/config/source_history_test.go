package config

import (
	"testing"

	"github.com/umayr/brine/internal/snapshothistory"
)

func TestSourceHistoryLimits(t *testing.T) {
	for _, tc := range []struct {
		name              string
		limits            snapshothistory.SourceArchiveLimits
		configured, valid bool
	}{
		{"disabled", snapshothistory.SourceArchiveLimits{}, false, true},
		{"no archive", snapshothistory.SourceArchiveLimits{Objects: 101}, false, false},
		{"defaults", snapshothistory.SourceArchiveLimits{}, true, true},
		{"custom", snapshothistory.SourceArchiveLimits{Objects: 200, Bytes: 300 << 20}, true, true},
		{"negative count", snapshothistory.SourceArchiveLimits{Objects: -1}, true, false},
		{"negative bytes", snapshothistory.SourceArchiveLimits{Bytes: -1}, true, false},
		{"count too high", snapshothistory.SourceArchiveLimits{Objects: snapshothistory.MaxSourceArchiveObjects + 1}, true, false},
		{"bytes too high", snapshothistory.SourceArchiveLimits{Bytes: snapshothistory.MaxSourceArchiveBytes + 1}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			if tc.configured {
				cfg.SourceHistoryDir, cfg.SourceHistoryKey = "/archive", "/keys/history"
			}
			cfg.SourceHistoryLimits = tc.limits
			if err := cfg.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate() = %v, valid expected: %v", err, tc.valid)
			}
		})
	}
}
