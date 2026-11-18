package detectionpack

import (
	"testing"

	"github.com/lycaon/lycaon/config"

	"github.com/lycaon/lycaon/internal/testutil"
)

func shippedPacks(t *testing.T) []Pack {
	t.Helper()
	entries, err := config.List(config.DetectionPacksDir)
	testutil.FailErr(t, "list bundled detection pack fixtures", err)
	var packs []Pack
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pack, warnings := ParsePack(bundledPackFiles(config.DetectionPacksDir.Join(entry.Name())))
		if len(warnings) != 0 || pack == nil {
			t.Fatalf("parse bundled detection pack %s: %v", entry.Name(), warnings)
		}
		packs = append(packs, *pack)
	}
	return packs
}

// deviceInput is one catalog load over the shipped packs plus device state.
func deviceInput(t *testing.T, configDir, projectDir string) Input {
	t.Helper()
	return Input{
		ConfigDir:   configDir,
		ProjectDir:  projectDir,
		Contributed: shippedPacks(t),
	}
}

// shippedInput is deviceInput with no device or project state.
func shippedInput(t *testing.T) Input {
	t.Helper()
	return deviceInput(t, "", "")
}
