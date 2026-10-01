package project

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/sandbox"
)

const (
	// HostProjectsDirName is the per-project host-data tree under the sidecar data dir.
	HostProjectsDirName = "projects"
	// HostPlanCriticDir is advisory plan-critic JSONL (not gate evidence).
	HostPlanCriticDir = "plan-critic"
)

// HostDataDir returns dataDir/projects/{projectID}; an empty dataDir means
// configdir.UserConfigDir, the sidecar data root holding store.db.
// Empty projectID returns "".
func HostDataDir(dataDir, projectID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return ""
	}
	base := strings.TrimSpace(dataDir)
	if base == "" {
		var err error
		base, err = configdir.UserConfigDir()
		if err != nil || base == "" {
			return ""
		}
	}
	return filepath.Join(base, HostProjectsDirName, projectID)
}

// EnsureHostDataDir creates HostDataDir at 0700 when missing.
func EnsureHostDataDir(dataDir, projectID string) (string, error) {
	dir := HostDataDir(dataDir, projectID)
	if dir == "" {
		return "", fmt.Errorf("project id required for host data dir")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create host data dir: %w", err)
	}
	return dir, nil
}

// HostSubdir returns EnsureHostDataDir/.../rel under the project host tree.
func HostSubdir(dataDir, projectID, rel string) (string, error) {
	root, err := EnsureHostDataDir(dataDir, projectID)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" || sandbox.HasParentTraversal(rel) {
		return "", fmt.Errorf("invalid host subdir %q", rel)
	}
	dir := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// RemoveHostDataDir best-effort deletes projects/{projectID} under the data dir.
func RemoveHostDataDir(dataDir, projectID string) error {
	dir := HostDataDir(dataDir, projectID)
	if dir == "" {
		return nil
	}
	return bloblifecycle.RemoveTree(filepath.Dir(filepath.Dir(dir)), dir)
}

// PathKeyedHostDataDir returns a stable host-data tree for a workspace path when
// no project_id is available (e.g. code_scans keyed by canonical_path).
func PathKeyedHostDataDir(dataDir, workspacePath string) string {
	workspacePath = filepath.Clean(strings.TrimSpace(workspacePath))
	if workspacePath == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(workspacePath))
	return HostDataDir(dataDir, "path-"+hex.EncodeToString(sum[:8]))
}

// OSVCacheDir returns the machine-wide OSV DB cache under the sidecar data dir
// (shared across projects — the zip is not project content).
func OSVCacheDir(dataDir string) string {
	base := strings.TrimSpace(dataDir)
	if base == "" {
		var err error
		base, err = configdir.UserConfigDir()
		if err != nil || base == "" {
			return ""
		}
	}
	return filepath.Join(base, enginepaths.CacheDirName, "osv")
}

// EnsureOSVCacheDir creates OSVCacheDir at 0750 when missing.
func EnsureOSVCacheDir(dataDir string) (string, error) {
	dir := OSVCacheDir(dataDir)
	if dir == "" {
		return "", fmt.Errorf("data dir required for osv cache")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create osv cache: %w", err)
	}
	return dir, nil
}

// RemoveDraftScratch deletes the project's engine-managed draft directory.
func RemoveDraftScratch(dataDir, projectID string) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil
	}
	base := strings.TrimSpace(dataDir)
	if base == "" {
		var err error
		base, err = configdir.UserConfigDir()
		if err != nil {
			return err
		}
	}
	return bloblifecycle.RemoveTree(base, enginepaths.DraftWorkspaceUnder(base, projectID))
}

// ReconcileHostStorage removes project and draft trees whose project IDs no
// longer exist in the durable registry.
func ReconcileHostStorage(dataDir string, liveProjectIDs map[string]struct{}) (int, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return 0, fmt.Errorf("project host reconciliation requires data dir")
	}
	var removed int
	var errs []error
	for _, root := range []string{
		filepath.Join(dataDir, HostProjectsDirName),
		enginepaths.DraftsRootUnder(dataDir),
	} {
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("read %s: %w", root, err))
			continue
		}
		for _, entry := range entries {
			if _, live := liveProjectIDs[entry.Name()]; live {
				continue
			}
			path := filepath.Join(root, entry.Name())
			if err := bloblifecycle.RemoveTree(dataDir, path); err != nil {
				errs = append(errs, fmt.Errorf("remove orphan %s: %w", path, err))
				continue
			}
			removed++
		}
	}
	return removed, errors.Join(errs...)
}
