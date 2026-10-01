package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestDBWipeScriptPreservesDenAppPreferences(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	onboardingPath := filepath.Join(
		configDir,
		"app-state-v1",
		"6f6e626f617264696e67.json",
	)
	projectMarker := filepath.Join(configDir, "projects", "stale")
	for path, content := range map[string]string{
		dbPath:         "store",
		onboardingPath: `{"key":"onboarding","value":{"firstRunSetupCompleted":true}}`,
		projectMarker:  "stale",
	} {
		contractcheck.FailErr(t, "create fixture directory", os.MkdirAll(filepath.Dir(path), 0o700))
		contractcheck.FailErr(t, "write fixture", os.WriteFile(path, []byte(content), 0o600))
	}

	binDir := filepath.Join(configDir, "bin")
	contractcheck.FailErr(t, "create fake bin", os.MkdirAll(binDir, 0o700))
	lsofPath := filepath.Join(binDir, "lsof")
	contractcheck.FailErr(t, "write fake lsof", os.WriteFile(lsofPath, []byte("#!/bin/sh\nexit 1\n"), 0o700))

	cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(root, "scripts", "db-wipe.sh"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"LYCAON_DB_PATH="+dbPath,
		"LYCAON_CONFIG_DIR="+configDir,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("db-wipe.sh failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("store survived wipe: %v", err)
	}
	if _, err := os.Stat(projectMarker); !os.IsNotExist(err) {
		t.Fatalf("store-keyed project state survived wipe: %v", err)
	}
	if _, err := os.Stat(onboardingPath); err != nil {
		t.Fatalf("onboarding preference was removed: %v", err)
	}
}
