package backup

import (
	"encoding/hex"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/localdata"
)

// RestorableRelPath accepts only paths emitted by archive creation: the flat
// durable files and descendants of durable directories.
func RestorableRelPath(rel string) bool {
	cleaned := filepath.ToSlash(filepath.Clean(rel))
	if cleaned == "" || cleaned == "." || cleaned != rel || strings.Contains(rel, "\\") {
		return false
	}
	if strings.HasPrefix(rel, enginepaths.WorkerBranchesDirName+"/") {
		return workerMetadataPath(rel, false)
	}
	for _, allowed := range localdata.BackupRelPaths() {
		if cleaned == filepath.ToSlash(filepath.Clean(allowed)) {
			return true
		}
	}
	for _, dir := range localdata.BackupRelDirs() {
		prefix := filepath.ToSlash(filepath.Clean(dir)) + "/"
		if strings.HasPrefix(cleaned, prefix) && len(cleaned) > len(prefix) {
			return true
		}
	}
	return false
}

// Branch trees are rebuildable; only the host's layout metadata is retained.
func workerMetadataPath(rel string, directory bool) bool {
	parts := strings.Split(rel, "/")
	if parts[0] != enginepaths.WorkerBranchesDirName {
		return true
	}
	if len(parts) == 1 {
		return directory
	}
	if len(parts[1]) != 16 {
		return false
	}
	if _, err := hex.DecodeString(parts[1]); err != nil || strings.ToLower(parts[1]) != parts[1] {
		return false
	}
	if len(parts) == 2 {
		return directory
	}
	if !enginepaths.IsJobMetaDirName(parts[2]) || parts[2] == enginepaths.JobMetaDirSuffix {
		return false
	}
	if len(parts) == 3 {
		return directory
	}
	return len(parts) == 4 && !directory && parts[3] == "state.json"
}

// RestorableRelDir accepts only the durable directories archived per file.
func RestorableRelDir(rel string) bool {
	cleaned := filepath.ToSlash(filepath.Clean(rel))
	for _, dir := range localdata.BackupRelDirs() {
		if cleaned == dir {
			return true
		}
	}
	return false
}
