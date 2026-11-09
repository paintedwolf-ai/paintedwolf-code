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
	if strings.HasPrefix(rel, enginepaths.WorkerRecoveryDirName+"/") {
		return durableWorkerRecoveryPath(rel, false)
	}
	if strings.HasPrefix(rel, enginepaths.WorkerBranchesDirName+"/") {
		return durableWorkerAdapterPath(rel, false) || workerMetadataPath(rel, false) || materializedBaseJobID(rel) != ""
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

func isLowerHex16(s string) bool {
	if len(s) != 16 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func isTransientRecoverySegment(seg string) bool {
	return seg == "" || strings.HasPrefix(seg, ".") || strings.HasSuffix(seg, ".tmp") || strings.Contains(seg, ".tmp.")
}

func durableWorkerRecoveryPath(rel string, directory bool) bool {
	parts := strings.Split(rel, "/")
	if len(parts) == 0 || parts[0] != enginepaths.WorkerRecoveryDirName {
		return false
	}
	if len(parts) == 1 {
		return directory
	}
	for _, seg := range parts[1:] {
		if isTransientRecoverySegment(seg) {
			return false
		}
	}
	if parts[1] == "mounts" || (parts[1] == "state" && len(parts) >= 3 && parts[2] == "mounts") {
		return false
	}
	return true
}

func durableWorkerAdapterPath(rel string, directory bool) bool {
	parts := strings.Split(rel, "/")
	if len(parts) < 2 || parts[0] != enginepaths.WorkerBranchesDirName || parts[1] != ".adapter" {
		return false
	}
	if len(parts) == 2 {
		return directory
	}
	for _, seg := range parts[2:] {
		if isTransientRecoverySegment(seg) {
			return false
		}
	}
	switch parts[2] {
	case "privategit":
		if len(parts) == 3 {
			return directory
		}
		if parts[3] != "retained-store" {
			return false
		}
		if len(parts) == 4 {
			return directory
		}
		if parts[4] == "imports" {
			return false
		}
		return true
	case "recovery":
		if len(parts) == 3 {
			return directory
		}
		if parts[3] == "mounts" || (parts[3] == "state" && len(parts) >= 5 && parts[4] == "mounts") {
			return false
		}
		return true
	default:
		if !isLowerHex16(parts[2]) {
			return false
		}
		if len(parts) == 3 {
			return directory
		}
		if parts[3] != "retained-git-store" {
			return false
		}
		if len(parts) == 4 {
			return directory
		}
		if parts[4] == "imports" {
			return false
		}
		return true
	}
}

// Branch trees are rebuildable when sealed by a checkpoint; host layout metadata is always retained.
func workerMetadataPath(rel string, directory bool) bool {
	parts := strings.Split(rel, "/")
	if parts[0] != enginepaths.WorkerBranchesDirName {
		return true
	}
	if len(parts) == 1 {
		return directory
	}
	if !isLowerHex16(parts[1]) {
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
	return len(parts) == 4 && !directory && (parts[3] == "state.json" || parts[3] == "materialized-git.json")
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
