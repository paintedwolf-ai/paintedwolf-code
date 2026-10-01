package backup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
)

// CleanupInterruptedTransfers runs with the exclusive store lease before serving.
// Published transactions and recovery snapshots use separate namespaces.
func CleanupInterruptedTransfers(configDir string) error {
	var result error
	for _, dir := range []string{configDir, filepath.Join(configDir, db.UpgradeRecoveryDirName)} {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, snapshotTempPrefix) &&
				!(strings.HasPrefix(name, ".backup-export-") && strings.HasSuffix(name, ".zip")) &&
				!(strings.HasPrefix(name, ".restore-upload-") && strings.HasSuffix(name, ".zip")) {
				continue
			}
			result = errors.Join(result, os.RemoveAll(filepath.Join(dir, name)))
		}
	}
	return result
}
