package enginepaths

import (
	"path/filepath"
	"strings"
)

// Names shared by state writers and confinement.
const (
	DraftsDirName            = "drafts"
	BrowserCacheDirName      = "browser-cache"
	DecideModelsDirName      = "decide-models"
	ModelfeedDirName         = "modelfeed"
	FetchCacheDirName        = "fetch-cache"
	PricingCacheDirName      = "pricing-cache"
	ExtensionsCacheDirName   = "extensions"
	ExtensionsMetaDirName    = "extensions-meta"
	SourceContentDirName     = "source-content"
	WorkerBaselinesDirName   = "worker-baselines"
	SourceObservationsDBName = "source-observations.db"
	RepoOrientationDirName   = "repo-orientation"
	// CacheDirName groups rebuildable caches that have their own subtree.
	CacheDirName              = "cache"
	SourceCatalogDirName      = "sourcecatalog"
	SessionWorktreesDirName   = "session-worktrees"
	SessionCheckpointsDirName = "session-checkpoints"
	SSHDirName                = "ssh"
	VerifyDetectFileName      = "verify-detect.json"
	ScratchDirName            = "scratch"
)

// SSHRootUnder returns host-managed SSH reference data.
func SSHRootUnder(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), SSHDirName)
}

// SSHKnownHostsMirrorUnder returns the read-only host-key mirror.
func SSHKnownHostsMirrorUnder(stateRoot string) string {
	return filepath.Join(SSHRootUnder(stateRoot), "known_hosts")
}

// DraftsRootUnder returns the draft-workspace parent under an engine state root.
func DraftsRootUnder(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), DraftsDirName)
}

// DraftWorkspaceUnder returns one draft project's workspace directory.
func DraftWorkspaceUnder(stateRoot, projectID string) string {
	return filepath.Join(DraftsRootUnder(stateRoot), strings.TrimSpace(projectID))
}

// BrowserCacheRootUnder returns the managed-browser cache directory.
func BrowserCacheRootUnder(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), BrowserCacheDirName)
}

// DecideModelsRootUnder returns the decision engine's checkpoint cache.
func DecideModelsRootUnder(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), DecideModelsDirName)
}

// ModelfeedRootUnder returns the model-feed cache directory.
func ModelfeedRootUnder(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), ModelfeedDirName)
}

// FetchCacheRootUnder returns the fetch-cache directory.
func FetchCacheRootUnder(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), FetchCacheDirName)
}

// RepoOrientationRootUnder returns rebuildable project orientation observations.
func RepoOrientationRootUnder(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), RepoOrientationDirName)
}

// SourceCatalogCacheRootUnder returns the rebuildable per-tree source catalog
// generations that search and the file picker read.
func SourceCatalogCacheRootUnder(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), CacheDirName, SourceCatalogDirName)
}

// PricingCacheRootUnder returns the pricing-feed disk cache directory.
func PricingCacheRootUnder(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), PricingCacheDirName)
}

// ExtensionsCacheRootUnder returns the content-addressed extension pack body cache.
func ExtensionsCacheRootUnder(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), ExtensionsCacheDirName)
}

// ExtensionsMetaRootUnder returns the community meta-pack cache directory.
func ExtensionsMetaRootUnder(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), ExtensionsMetaDirName)
}

// VerifyDetectPathUnder returns the verify-command detection cache file.
func VerifyDetectPathUnder(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), VerifyDetectFileName)
}

// SessionWorktreesRootUnder returns worktree storage outside project roots.
func SessionWorktreesRootUnder(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), SessionWorktreesDirName)
}

// ProjectWorktreeDir returns the per-project parent for session worktrees, keyed by ProjectKey.
func ProjectWorktreeDir(worktreeRoot, projectDir string) string {
	return filepath.Join(strings.TrimSpace(worktreeRoot), ProjectKey(projectDir))
}

// SessionWorktreeDir returns one session's worktree directory.
func SessionWorktreeDir(worktreeRoot, projectDir, sessionID string) string {
	return filepath.Join(ProjectWorktreeDir(worktreeRoot, projectDir), strings.TrimSpace(sessionID))
}

// SessionCheckpointsRootUnder returns checkpoint storage outside project roots.
// Checkpoints remain unreadable to confined commands.
func SessionCheckpointsRootUnder(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), SessionCheckpointsDirName)
}

// ProjectCheckpointDir returns checkpoint storage keyed by source root.
func ProjectCheckpointDir(checkpointRoot, projectDir string) string {
	return filepath.Join(strings.TrimSpace(checkpointRoot), ProjectKey(projectDir))
}

// ScratchRootUnder returns the parent of every session's scratch folder. Only
// the engine writes it, so no agent can replace another session's folder.
func ScratchRootUnder(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), ScratchDirName)
}

// SessionScratchUnder returns one session's private scratch folder. Every
// session, coordinator or worker, has its own folder directly under the root.
func SessionScratchUnder(stateRoot, sessionID string) string {
	return filepath.Join(ScratchRootUnder(stateRoot), strings.TrimSpace(sessionID))
}

// AgentWorkspaceRootsUnder returns state subtrees granted to confined commands.
func AgentWorkspaceRootsUnder(stateRoot string) []string {
	return []string{
		DraftsRootUnder(stateRoot),
		WorkerBranchesRootUnder(stateRoot),
		SessionWorktreesRootUnder(stateRoot),
		BrowserCacheRootUnder(stateRoot),
		// Confined commands read the host-key mirror.
		SSHRootUnder(stateRoot),
	}
}
