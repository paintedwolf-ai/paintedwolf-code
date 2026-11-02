package backup

import "fmt"

// Version 1 shipped before configured command programs joined the backup set.
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

func archiveScope(version int, currentFiles, currentDirectories []string) ([]string, []string, error) {
	switch version {
	case 1:
		return archiveV1Files, archiveV1Directories, nil
	case FormatVersion:
		return currentFiles, currentDirectories, nil
	default:
		return nil, nil, &InvalidError{Detail: fmt.Sprintf("unsupported format_version %d", version)}
	}
}

// The validated legacy snapshot has no configured programs. Include their new
// path in the restore transaction, preserving any live file in its pre-image.
func upgradeArchiveScope(manifest Manifest) Manifest {
	if manifest.FormatVersion == 1 {
		manifest.FormatVersion = FormatVersion
		manifest.ReplaceRelPaths = append(append([]string(nil), manifest.ReplaceRelPaths...), "command-programs.yaml")
	}
	return manifest
}
