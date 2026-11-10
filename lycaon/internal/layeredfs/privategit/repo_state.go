package privategit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/layeredfs"
)

const (
	durableRepoFormatVersion  = 1
	maxDurableRepoRecordBytes = 64 << 10
)

type durableRepoStatus string

const (
	repoStatusUnknown      durableRepoStatus = ""
	repoStatusNoSourceRepo durableRepoStatus = "no_source_repo"
	repoStatusEstablished  durableRepoStatus = "established"
	repoStatusFailed       durableRepoStatus = "failed"
)

type durableRepoRecord struct {
	FormatVersion  int               `json:"format_version"`
	RepoRel        string            `json:"repo_rel"`
	Status         durableRepoStatus `json:"status"`
	PrivateGitDir  string            `json:"private_git_dir,omitempty"`
	LeaseID        string            `json:"lease_id,omitempty"`
	InitialHeadOID string            `json:"initial_head_oid,omitempty"`
}

func durableRepoStatePath(mgr *Manager, viewID, repoRel string) string {
	if mgr == nil || strings.TrimSpace(mgr.cfg.BackingDir) == "" || strings.TrimSpace(viewID) == "" {
		return ""
	}
	return filepath.Join(mgr.cfg.BackingDir, "views", viewID, "repo-state", sanitizeRepoKey(repoRel)+".json")
}

func expectedPrivateGitLocations(mgr *Manager, view *managedView, viewID, repoRel string) []string {
	if mgr == nil || strings.TrimSpace(mgr.cfg.BackingDir) == "" || strings.TrimSpace(viewID) == "" {
		return nil
	}
	repoRel = cleanSlashRel(repoRel)
	key := sanitizeRepoKey(repoRel)
	backingGitDir := filepath.Join(mgr.cfg.BackingDir, "views", viewID, "private-git", key, ".git")
	locs := []string{filepath.Clean(backingGitDir)}
	if mgr.canonBackingDir != "" {
		locs = append(locs, filepath.Clean(filepath.Join(mgr.canonBackingDir, "views", viewID, "private-git", key, ".git")))
	}
	if view != nil && !view.kernelMounted && view.worktreeDir != "" && filepath.Clean(view.worktreeDir) != filepath.Clean(view.trunkDir) {
		targetWT := view.worktreeDir
		if repoRel != "." {
			targetWT = filepath.Join(view.worktreeDir, filepath.FromSlash(repoRel))
		}
		locs = append(locs, filepath.Clean(filepath.Join(targetWT, ".git")))
		if view.canonWorktreeDir != "" {
			canonTargetWT := view.canonWorktreeDir
			if repoRel != "." {
				canonTargetWT = filepath.Join(view.canonWorktreeDir, filepath.FromSlash(repoRel))
			}
			locs = append(locs, filepath.Clean(filepath.Join(canonTargetWT, ".git")))
		}
	}
	return locs
}

func isAllowedPrivateGitLocation(mgr *Manager, view *managedView, viewID, repoRel, candidate string) bool {
	if strings.TrimSpace(candidate) == "" || !filepath.IsAbs(candidate) {
		return false
	}
	cleanCand := filepath.Clean(candidate)
	for _, allowed := range expectedPrivateGitLocations(mgr, view, viewID, repoRel) {
		if allowed != "" && cleanCand == filepath.Clean(allowed) {
			return true
		}
	}
	return false
}

func validateDurableRepoRecord(mgr *Manager, view *managedView, viewID, expectedRepoRel string, rec durableRepoRecord) error {
	expectedRepoRel = cleanSlashRel(expectedRepoRel)
	if rec.FormatVersion != durableRepoFormatVersion {
		return fmt.Errorf("%w: %w: unsupported format_version %d", layeredfs.ErrGitPending, ErrInvalidRepoStateRecord, rec.FormatVersion)
	}
	if strings.TrimSpace(rec.RepoRel) == "" || cleanSlashRel(rec.RepoRel) != expectedRepoRel {
		return fmt.Errorf("%w: %w: repo_rel %q does not match expected %q", layeredfs.ErrGitPending, ErrInvalidRepoStateRecord, rec.RepoRel, expectedRepoRel)
	}
	switch rec.Status {
	case repoStatusEstablished:
		if !isAllowedPrivateGitLocation(mgr, view, viewID, expectedRepoRel, rec.PrivateGitDir) {
			return fmt.Errorf("%w: %w: invalid private_git_dir %q for %q", layeredfs.ErrGitPending, ErrInvalidRepoStateRecord, rec.PrivateGitDir, expectedRepoRel)
		}
		if rec.InitialHeadOID != "" && (len(rec.InitialHeadOID) != 40 || !isHexString(rec.InitialHeadOID)) {
			return fmt.Errorf("%w: %w: invalid initial_head_oid %q", layeredfs.ErrGitPending, ErrInvalidRepoStateRecord, rec.InitialHeadOID)
		}
		if strings.TrimSpace(rec.LeaseID) == "" {
			return fmt.Errorf("%w: %w: established repository record missing lease_id", layeredfs.ErrGitPending, ErrInvalidRepoStateRecord)
		}
	case repoStatusNoSourceRepo:
		if rec.PrivateGitDir != "" || rec.LeaseID != "" || rec.InitialHeadOID != "" {
			return fmt.Errorf("%w: %w: no_source_repo record cannot carry private git state", layeredfs.ErrGitPending, ErrInvalidRepoStateRecord)
		}
	case repoStatusFailed:
	default:
		return fmt.Errorf("%w: %w: unknown status %q", layeredfs.ErrGitPending, ErrInvalidRepoStateRecord, rec.Status)
	}
	return nil
}

func readDurableRepoRecord(mgr *Manager, view *managedView, viewID, repoRel string) (durableRepoRecord, bool, error) {
	p := durableRepoStatePath(mgr, viewID, repoRel)
	if p == "" {
		return durableRepoRecord{}, false, fmt.Errorf("%w: %w: empty repo state path", layeredfs.ErrGitPending, ErrInvalidRepoStateRecord)
	}
	info, err := os.Lstat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return durableRepoRecord{}, false, nil
		}
		return durableRepoRecord{}, false, fmt.Errorf("%w: %w: lstat %s: %v", layeredfs.ErrGitPending, ErrInvalidRepoStateRecord, p, err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxDurableRepoRecordBytes {
		return durableRepoRecord{}, false, fmt.Errorf("%w: %w: invalid file mode or size for %s", layeredfs.ErrGitPending, ErrInvalidRepoStateRecord, p)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return durableRepoRecord{}, false, fmt.Errorf("%w: %w: read %s: %v", layeredfs.ErrGitPending, ErrInvalidRepoStateRecord, p, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var rec durableRepoRecord
	if err := dec.Decode(&rec); err != nil {
		return durableRepoRecord{}, false, fmt.Errorf("%w: %w: decode %s: %v", layeredfs.ErrGitPending, ErrInvalidRepoStateRecord, p, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return durableRepoRecord{}, false, fmt.Errorf("%w: %w: trailing data in %s", layeredfs.ErrGitPending, ErrInvalidRepoStateRecord, p)
	}
	if err := validateDurableRepoRecord(mgr, view, viewID, repoRel, rec); err != nil {
		return durableRepoRecord{}, false, err
	}
	return rec, true, nil
}

func writeDurableRepoRecord(mgr *Manager, view *managedView, viewID string, rec durableRepoRecord) error {
	rec.FormatVersion = durableRepoFormatVersion
	rec.RepoRel = cleanSlashRel(rec.RepoRel)
	if err := validateDurableRepoRecord(mgr, view, viewID, rec.RepoRel, rec); err != nil {
		return err
	}
	if mgr != nil && mgr.cfg.FailRepoStateWriteHook != nil {
		if err := mgr.cfg.FailRepoStateWriteHook(viewID, rec.RepoRel, string(rec.Status)); err != nil {
			return fmt.Errorf("%w: %w: %v", layeredfs.ErrGitPending, ErrInvalidRepoStateRecord, err)
		}
	}
	p := durableRepoStatePath(mgr, viewID, rec.RepoRel)
	if p == "" {
		return fmt.Errorf("%w: %w: empty repo state path", layeredfs.ErrGitPending, ErrInvalidRepoStateRecord)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("%w: %w: mkdir %s: %v", layeredfs.ErrGitPending, ErrInvalidRepoStateRecord, filepath.Dir(p), err)
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("%w: %w: marshal: %v", layeredfs.ErrGitPending, ErrInvalidRepoStateRecord, err)
	}
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(p),
		Source:   bytes.NewReader(raw),
		Mode:     0o600,
		DirMode:  0o755,
	}); err != nil {
		return fmt.Errorf("%w: %w: write %s: %v", layeredfs.ErrGitPending, ErrInvalidRepoStateRecord, p, err)
	}
	return nil
}

func repoInitIntentPath(mgr *Manager, viewID, repoRel string) string {
	if mgr == nil || strings.TrimSpace(mgr.cfg.BackingDir) == "" || strings.TrimSpace(viewID) == "" {
		return ""
	}
	return filepath.Join(mgr.cfg.BackingDir, "views", viewID, "repo-intent", sanitizeRepoKey(repoRel)+".intent")
}

func writeRepoInitIntent(mgr *Manager, viewID, repoRel string) error {
	p := repoInitIntentPath(mgr, viewID, repoRel)
	if p == "" {
		return fmt.Errorf("empty repo init intent path")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(p),
		Source:   bytes.NewReader([]byte("1\n")),
		Mode:     0o600,
		DirMode:  0o755,
	})
	return err
}

func removeRepoInitIntent(mgr *Manager, viewID, repoRel string) {
	if p := repoInitIntentPath(mgr, viewID, repoRel); p != "" {
		_ = os.Remove(p)
	}
}

func discoverPriorPrivateStateEvidence(mgr *Manager, view *managedView, viewID, repoRel string) (string, bool) {
	if mgr == nil || strings.TrimSpace(mgr.cfg.BackingDir) == "" || strings.TrimSpace(viewID) == "" {
		return "", false
	}
	repoRel = cleanSlashRel(repoRel)
	backingGitDir := filepath.Join(mgr.cfg.BackingDir, "views", viewID, "private-git", sanitizeRepoKey(repoRel), ".git")
	if info, err := os.Lstat(backingGitDir); err == nil && info.IsDir() {
		return backingGitDir, true
	}
	if view != nil && !view.kernelMounted && view.worktreeDir != "" && filepath.Clean(view.worktreeDir) != filepath.Clean(view.trunkDir) {
		targetWT := view.worktreeDir
		if repoRel != "." {
			targetWT = filepath.Join(view.worktreeDir, filepath.FromSlash(repoRel))
		}
		wtGitDir := filepath.Join(targetWT, ".git")
		if info, err := os.Lstat(wtGitDir); err == nil && info.IsDir() {
			return wtGitDir, true
		}
	}
	if intentPath := repoInitIntentPath(mgr, viewID, repoRel); intentPath != "" {
		if _, err := os.Lstat(intentPath); err == nil {
			return "", true
		}
	}
	stageBase := filepath.Join(mgr.cfg.BackingDir, "views", viewID, "git-staging")
	if entries, err := os.ReadDir(stageBase); err == nil && len(entries) > 0 {
		return "", true
	}
	return "", false
}

func objectExistsInObjectsDir(objDir, oid string) bool {
	if strings.TrimSpace(objDir) == "" || len(oid) != 40 || !isHexString(oid) {
		return false
	}
	if _, err := os.Stat(filepath.Join(objDir, oid[:2], oid[2:])); err == nil {
		return true
	}
	packDir := filepath.Join(objDir, "pack")
	entries, err := os.ReadDir(packDir)
	if err != nil {
		return false
	}
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".idx") {
			continue
		}
		base := strings.TrimSuffix(de.Name(), ".idx")
		if _, err := os.Stat(filepath.Join(packDir, base+".pack")); err != nil {
			continue
		}
		members, comp, err := parsePackIndexOIDs(context.Background(), objDir, filepath.Join(packDir, de.Name()))
		if err != nil {
			continue
		}
		if comp != nil && comp.containsHex(oid) {
			return true
		}
		if _, ok := members[oid]; ok {
			return true
		}
	}
	return false
}

func objectExistsInAlternates(gitDir, oid string) bool {
	for _, alt := range alternateObjectDirs(gitDir) {
		if objectExistsInObjectsDir(alt, oid) {
			return true
		}
	}
	return false
}

func alternateObjectDirs(gitDir string) []string {
	raw, err := os.ReadFile(filepath.Join(gitDir, "objects", "info", "alternates"))
	if err != nil {
		return nil
	}
	var dirs []string
	for _, line := range strings.Split(string(raw), "\n") {
		alt := strings.TrimSpace(line)
		if alt == "" || strings.HasPrefix(alt, "#") {
			continue
		}
		if !filepath.IsAbs(alt) {
			alt = filepath.Join(gitDir, "objects", alt)
		}
		dirs = append(dirs, filepath.Clean(alt))
	}
	return dirs
}

func objectDirsForPrivateGitDir(gitDir, storeObjectsDir string) []string {
	dirs := []string{filepath.Join(gitDir, "objects")}
	dirs = append(dirs, alternateObjectDirs(gitDir)...)
	if storeObjectsDir != "" {
		dirs = append(dirs, storeObjectsDir)
	}
	return dirs
}

func verifyEstablishedPrivateRepo(
	mgr *Manager,
	view *managedView,
	viewID, repoRel string,
	repo *initializedRepo,
	durable durableRepoRecord,
	hasDurable bool,
	fallbackGitDir string,
) (string, string, string, error) {
	repoRel = cleanSlashRel(repoRel)
	gitDir := ""
	leaseID := durable.LeaseID
	headOID := durable.InitialHeadOID
	if repo != nil {
		if repo.privateGitDir != "" {
			gitDir = repo.privateGitDir
		}
		if repo.leaseID != "" {
			leaseID = repo.leaseID
		}
		if repo.initialHeadOID != "" {
			headOID = repo.initialHeadOID
		}
	}
	if gitDir == "" {
		gitDir = durable.PrivateGitDir
	}
	if gitDir == "" {
		gitDir = fallbackGitDir
	}
	if !isAllowedPrivateGitLocation(mgr, view, viewID, repoRel, gitDir) {
		return "", "", "", fmt.Errorf("%w: %w: invalid established private repository path %q", layeredfs.ErrGitPending, ErrInvalidRepoStateRecord, gitDir)
	}
	if _, err := os.Stat(filepath.Join(gitDir, "HEAD")); err != nil {
		return "", "", "", fmt.Errorf("%w: established private repository HEAD unavailable: %w", layeredfs.ErrGitPending, err)
	}
	if mgr == nil || mgr.store == nil {
		return "", "", "", fmt.Errorf("%w: %w: retained object store unavailable for established private repository", layeredfs.ErrGitPending, ErrCorruptObjectStore)
	}
	holder := viewID + ":" + repoRel
	if hasDurable && repo != nil && repo.leaseID != "" && durable.LeaseID != "" && durable.LeaseID != repo.leaseID {
		return "", "", "", fmt.Errorf("%w: %w: durable lease_id %q does not match active lease %q", layeredfs.ErrGitPending, ErrCorruptObjectStore, durable.LeaseID, repo.leaseID)
	}
	if !hasDurable && strings.TrimSpace(leaseID) == "" {
		tipOIDs, _, err := collectCapturedRefAndIndexOIDs(context.Background(), gitDir)
		if err != nil || len(tipOIDs) == 0 {
			return "", "", "", fmt.Errorf("%w: %w: recover private repository refs for %q: %v", layeredfs.ErrGitPending, ErrCorruptObjectStore, repoRel, err)
		}
		_, reachable, err := mgr.store.ImportRepositoryObjects(context.Background(), objectDirsForPrivateGitDir(gitDir, mgr.store.ObjectsDir()), tipOIDs)
		if err != nil {
			return "", "", "", fmt.Errorf("%w: %w: recover private repository objects for %q: %v", layeredfs.ErrGitPending, ErrCorruptObjectStore, repoRel, err)
		}
		leaseID, err = mgr.store.RetainForView(holder, reachable)
		if err != nil {
			return "", "", "", fmt.Errorf("%w: %w: reacquire private repository lease for %q: %v", layeredfs.ErrGitPending, ErrCorruptObjectStore, repoRel, err)
		}
	} else if repo == nil || repo.leaseID == "" {
		if err := reacquirePersistedStoreLease(mgr.store, holder, leaseID); err != nil {
			return "", "", "", fmt.Errorf("%w: %w: %v", layeredfs.ErrGitPending, ErrCorruptObjectStore, err)
		}
	}
	if !storeLeaseBoundAndIntactOnDisk(mgr.store, holder, leaseID) {
		return "", "", "", fmt.Errorf("%w: %w: retained object store lease %q unavailable for established private repository", layeredfs.ErrGitPending, ErrCorruptObjectStore, leaseID)
	}
	if headOID == "" {
		_, headOID, _, _ = resolveHeadState(gitDir)
	}
	if len(headOID) == 40 && isHexString(headOID) {
		if !storeHasObjectOnDisk(mgr.store, headOID) &&
			!objectExistsInObjectsDir(filepath.Join(gitDir, "objects"), headOID) &&
			!objectExistsInAlternates(gitDir, headOID) {
			return "", "", "", fmt.Errorf("%w: %w: initial HEAD object %s missing from retained and private object stores", layeredfs.ErrGitPending, ErrCorruptObjectStore, headOID)
		}
	}
	tipOIDs, _, rErr := collectCapturedRefAndIndexOIDs(context.Background(), gitDir)
	if rErr != nil {
		return "", "", "", fmt.Errorf("%w: %w: inspect established private repository refs/index: %v", layeredfs.ErrGitPending, ErrCorruptObjectStore, rErr)
	}
	for _, refOID := range tipOIDs {
		if len(refOID) == 40 && isHexString(refOID) {
			if !storeHasObjectOnDisk(mgr.store, refOID) &&
				!objectExistsInObjectsDir(filepath.Join(gitDir, "objects"), refOID) &&
				!objectExistsInAlternates(gitDir, refOID) {
				return "", "", "", fmt.Errorf("%w: %w: ref object %s missing from retained and private object stores", layeredfs.ErrGitPending, ErrCorruptObjectStore, refOID)
			}
		}
	}
	return gitDir, leaseID, headOID, nil
}
