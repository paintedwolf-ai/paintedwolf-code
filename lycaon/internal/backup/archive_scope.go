package backup

import (
	"fmt"
	"slices"

	"github.com/lycaon/lycaon/internal/enginepaths"
)

// Version 1 shipped before configured command programs and worker recovery joined the backup set.
var archiveV1Files = []string{
	"approvals.yaml", "file-summaries.yaml", "history-retention.json", "limits.yaml",
	"logview.yaml", "model-policy.yaml", "power.yaml", "pricing.yaml", "providers.local.yaml",
	"review.yaml", "scan-hints.yaml", "scanners.yaml", "security-scanners.yaml", "settings.yaml",
	"store.db", "trust-surfaces.yaml", "web-research-config.yaml",
}

var archiveV1Directories = []string{
	"app-state-v1", "document-outbox", "drafts", "projects", "session-checkpoints",
	"source-content", "worker-baselines", "worker-branches",
}

// Version 2 added configured command programs.
var archiveV2Files = append(slices.Clone(archiveV1Files), "command-programs.yaml")

var archiveV2Directories = archiveV1Directories

func archiveScope(version int, currentFiles, currentDirectories []string) ([]string, []string, error) {
	switch version {
	case 1:
		return archiveV1Files, archiveV1Directories, nil
	case 2:
		return archiveV2Files, archiveV2Directories, nil
	case FormatVersion:
		return currentFiles, currentDirectories, nil
	default:
		return nil, nil, &InvalidError{Detail: fmt.Sprintf("unsupported format_version %d", version)}
	}
}

// An earlier snapshot lacks the paths later formats added. Restoring it replaces them too, so their
// absence is restored and any live content stays in the transaction's pre-image.
func upgradeArchiveScope(manifest Manifest) Manifest {
	if manifest.FormatVersion >= FormatVersion {
		return manifest
	}
	manifest.ReplaceRelPaths = append([]string(nil), manifest.ReplaceRelPaths...)
	manifest.ReplaceRelDirs = append([]string(nil), manifest.ReplaceRelDirs...)
	if manifest.FormatVersion == 1 && !slices.Contains(manifest.ReplaceRelPaths, "command-programs.yaml") {
		manifest.ReplaceRelPaths = append(manifest.ReplaceRelPaths, "command-programs.yaml")
	}
	if !slices.Contains(manifest.ReplaceRelDirs, enginepaths.WorkerRecoveryDirName) {
		manifest.ReplaceRelDirs = append(manifest.ReplaceRelDirs, enginepaths.WorkerRecoveryDirName)
		slices.Sort(manifest.ReplaceRelDirs)
	}
	manifest.FormatVersion = FormatVersion
	return manifest
}
