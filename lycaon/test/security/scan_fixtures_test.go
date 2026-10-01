package security

import (
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
)

// Empty catalogs isolate finding counts from bundled hints and suppressions.
func stageEmptyHintsAndSuppressions(t *testing.T) {
	t.Helper()
	configtest.Overlay(t, map[config.Rel]string{
		config.ScanHints:   "hints: {}\n",
		config.ScanIgnores: "version: 1\nfindings: []\n",
	})
}
