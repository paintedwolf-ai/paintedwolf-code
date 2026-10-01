package backup

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/hostidentity"
	"github.com/lycaon/lycaon/internal/localdata"
)

// processStateFileNames are live process state and device identity that a configured database must not replace.
var processStateFileNames = map[string]struct{}{
	"api.token":           {},
	hostidentity.FileName: {},
	"daemon.json":         {},
}

// Host configuration determines the live database filename.
func liveStoreFilename(configDir, path string) (string, error) {
	if path == "" {
		return storeRelPath, nil
	}
	root, err := filepath.Abs(configDir)
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	name := filepath.Base(absolute)
	if filepath.Dir(absolute) != root || !filepath.IsLocal(name) || name == "." || strings.Contains(name, "\\") {
		return "", fmt.Errorf("backup: configured database must be a file under the data directory")
	}
	if name != storeRelPath {
		_, reserved := processStateFileNames[name]
		if reserved || name == PendingMarkerName || name == db.UpgradeRecoveryDirName || strings.HasPrefix(name, localdata.RestoreStagingDirPrefix) || strings.HasPrefix(name, localdata.RestorePreImageDirPrefix) || RestorableRelPath(name) || RestorableRelDir(name) || localdata.IsCredentialRel(name) {
			return "", fmt.Errorf("backup: configured database conflicts with a durable configuration path")
		}
		for _, suffix := range db.StoreSidecarSuffixes() {
			if strings.HasSuffix(name, suffix) {
				return "", fmt.Errorf("backup: configured database cannot use a journal filename")
			}
		}
	}
	return name, nil
}

func (m PendingMarker) liveRelPath(rel string) string {
	if rel == storeRelPath {
		return m.LiveStoreFilename
	}
	return rel
}

func recoverySourceRelPath(rel, liveName string) string {
	if rel == storeRelPath {
		return liveName
	}
	for _, suffix := range db.StoreSidecarSuffixes() {
		if rel == storeRelPath+suffix {
			return liveName + suffix
		}
	}
	return rel
}
