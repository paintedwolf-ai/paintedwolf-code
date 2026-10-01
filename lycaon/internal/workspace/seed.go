package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
)

const (
	seedFormatVersion = 1
	seedCurrentFile   = "CURRENT"
	seedManifestFile  = "manifest.json"
	seedGenerations   = "generations"
	seedLocksDir      = ".locks"
	seedLastUsedFile  = "LAST_USED"
	seedRetention     = 30 * 24 * time.Hour
)

type fileIdentity struct {
	Inode        uint64 `json:"inode,omitempty"`
	ChangeSecond int64  `json:"change_second,omitempty"`
	ChangeNano   int64  `json:"change_nano,omitempty"`
}

type seedEntry struct {
	Kind       string       `json:"kind"`
	Size       int64        `json:"size,omitempty"`
	Mode       uint32       `json:"mode"`
	ModTime    int64        `json:"mod_time"`
	Identity   fileIdentity `json:"identity,omitempty"`
	LinkTarget string       `json:"link_target,omitempty"`
}

type seedManifest struct {
	Format     int                  `json:"format"`
	SourceRoot string               `json:"source_root"`
	Entries    map[string]seedEntry `json:"entries"`
}

// SeedCacheStatus describes one retained, rebuildable bridge cache.
type SeedCacheStatus struct {
	ID             string
	SourceRoot     string
	LogicalBytes   int64
	AllocatedBytes int64
	LastUsed       time.Time
}

type seedLockSet struct {
	mu    sync.Mutex
	locks map[string]*sync.RWMutex
}

func newSeedLockSet() seedLockSet {
	return seedLockSet{locks: make(map[string]*sync.RWMutex)}
}

func (set *seedLockSet) forKey(key string) *sync.RWMutex {
	set.mu.Lock()
	defer set.mu.Unlock()
	lock := set.locks[key]
	if lock == nil {
		lock = &sync.RWMutex{}
		set.locks[key] = lock
	}
	return lock
}

func (m *Manager) provisionRoot(ctx context.Context, sourceRoot, branchRoot string) error {
	reportPreparation(ctx, PreparationProgress{Stage: "probing"})
	strategy, err := m.chooseProvisionStrategy(ctx, sourceRoot, branchRoot)
	if err != nil {
		return classifyProvisionError(err)
	}
	base := PreparationProgress{Strategy: strategy}
	if strings.TrimSpace(m.seedRoot) != "" {
		if err := m.evictExpiredSeeds(ctx, enginepaths.ProjectKey(sourceRoot)); err != nil {
			slog.WarnContext(ctx, "evict expired worker caches", "err", err)
		}
	}
	if strategy != ProvisionBridgeCoW {
		base.Stage = "snapshotting_branch"
		reportPreparation(ctx, base)
		result, mirrorErr := mirrorTree(ctx, sourceRoot, branchRoot, mirrorOptions{
			phase: "worker_branch_" + string(strategy), tolerateChange: true, preparation: base,
		})
		if mirrorErr != nil {
			return classifyProvisionError(fmt.Errorf("snapshot worker branch: %w", mirrorErr))
		}
		if err := validateCompleteMirror(result); err != nil {
			return err
		}
		logProvisionResult(ctx, sourceRoot, strategy, false, result)
		if err := RemoveSeed(ctx, m.seedRoot, sourceRoot); err != nil {
			slog.WarnContext(ctx, "remove worker cache no longer useful", "source", sourceRoot, "err", err)
		}
		return nil
	}

	base.Stage = "surveying_source"
	reportPreparation(ctx, base)
	refreshed, err := m.refreshSeedLocked(ctx, sourceRoot, base)
	if err != nil {
		return classifyProvisionError(fmt.Errorf("refresh worker seed: %w", err))
	}

	key := enginepaths.ProjectKey(sourceRoot)
	inProcess := m.locks.forKey(key)
	inProcess.RLock()
	defer inProcess.RUnlock()
	release, err := lockSeedAcrossProcesses(ctx, m.seedRoot, key, false)
	if err != nil {
		return err
	}
	defer release()
	seedDir := enginepaths.ProjectSeedDir(m.seedRoot, sourceRoot)
	_, seedTree, manifest := loadCurrentSeed(seedDir)
	if seedTree == "" {
		return fmt.Errorf("current worker seed is unavailable")
	}
	base.Stage = "snapshotting_branch"
	if manifest.Entries != nil {
		base.TotalBytes = regularBytes(manifest)
	}
	reportPreparation(ctx, base)
	result, err := mirrorTree(ctx, seedTree, branchRoot, mirrorOptions{
		phase: "worker_branch_bridge_cow", preparation: base,
	})
	if err != nil {
		return classifyProvisionError(fmt.Errorf("snapshot worker branch: %w", err))
	}
	touchSeedLastUsed(seedDir)
	logProvisionResult(ctx, sourceRoot, strategy, refreshed, result)
	return nil
}

func logProvisionResult(ctx context.Context, sourceRoot string, strategy ProvisionStrategy, refreshed bool, result mirrorResult) {
	slog.InfoContext(ctx, "prepared isolated worker workspace",
		"source", sourceRoot,
		"strategy", strategy,
		"seed_refreshed", refreshed,
		"files", result.Files,
		"bytes", result.Bytes,
		"cloned_files", result.ClonedFiles,
		"cloned_bytes", result.ClonedBytes,
		"copied_bytes", result.CopiedBytes,
	)
}

func (m *Manager) refreshSeedLocked(ctx context.Context, sourceRoot string, progress PreparationProgress) (bool, error) {
	key := enginepaths.ProjectKey(sourceRoot)
	inProcess := m.locks.forKey(key)
	inProcess.Lock()
	defer inProcess.Unlock()
	release, err := lockSeedAcrossProcesses(ctx, m.seedRoot, key, true)
	if err != nil {
		return false, err
	}
	defer release()
	return m.refreshSeed(ctx, sourceRoot, progress)
}

func lockSeedAcrossProcesses(ctx context.Context, seedRoot, key string, exclusive bool) (func(), error) {
	path := filepath.Join(seedRoot, seedLocksDir, key+".lock")
	file, err := filelock.Open(path)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var locked bool
		if exclusive {
			locked, err = filelock.TryExclusive(file)
		} else {
			locked, err = filelock.TryShared(file)
		}
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if locked {
			return func() {
				_ = filelock.Unlock(file)
				_ = file.Close()
			}, nil
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *Manager) refreshSeed(ctx context.Context, sourceRoot string, progress PreparationProgress) (bool, error) {
	cleanRoot, err := absDir(sourceRoot)
	if err != nil {
		return false, err
	}
	sourceRoot = cleanRoot
	seedDir := enginepaths.ProjectSeedDir(m.seedRoot, sourceRoot)
	if err := os.MkdirAll(filepath.Join(seedDir, seedGenerations), 0o700); err != nil {
		return false, err
	}

	manifest, err := scanSeedSource(ctx, sourceRoot)
	if err != nil {
		return false, err
	}
	currentGeneration, currentTree, currentManifest := loadCurrentSeed(seedDir)
	progress.Stage = "materializing_seed"
	progress.TotalBytes = regularBytes(manifest)
	reportPreparation(ctx, progress)
	if currentGeneration != "" && manifestsEqual(manifest, currentManifest) {
		cleanupSeedGenerations(seedDir, currentGeneration)
		touchSeedLastUsed(seedDir)
		return false, nil
	}
	required := changedRegularBytes(manifest, currentManifest)
	if err := m.ensureSeedCapacity(ctx, required, enginepaths.ProjectKey(sourceRoot)); err != nil {
		return false, err
	}

	generation := uuid.NewString()
	staging := filepath.Join(seedDir, seedGenerations, ".staging-"+generation)
	final := filepath.Join(seedDir, seedGenerations, generation)
	if err := os.RemoveAll(staging); err != nil {
		return false, err
	}
	stagingTree := filepath.Join(staging, "tree")
	result, err := mirrorTree(ctx, sourceRoot, stagingTree, mirrorOptions{
		phase:          "seed_refresh",
		tolerateChange: true,
		preparation:    progress,
		fileSource: func(rel, sourcePath string) string {
			if currentTree == "" || currentManifest.Entries == nil {
				return sourcePath
			}
			if old, ok := currentManifest.Entries[rel]; ok && old == manifest.Entries[rel] {
				candidate := filepath.Join(currentTree, filepath.FromSlash(rel))
				if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
					return candidate
				}
			}
			return sourcePath
		},
	})
	if err != nil {
		_ = os.RemoveAll(staging)
		return false, err
	}
	if err := validateCompleteMirror(result); err != nil {
		_ = os.RemoveAll(staging)
		return false, err
	}
	if err := writeSeedManifest(filepath.Join(staging, seedManifestFile), manifest); err != nil {
		_ = os.RemoveAll(staging)
		return false, err
	}
	if err := os.Rename(staging, final); err != nil {
		_ = os.RemoveAll(staging)
		return false, err
	}
	if err := publishCurrentGeneration(seedDir, generation); err != nil {
		_ = os.RemoveAll(final)
		return false, err
	}
	cleanupSeedGenerations(seedDir, generation)
	touchSeedLastUsed(seedDir)
	slog.InfoContext(ctx, "refreshed central worker seed",
		"source", sourceRoot,
		"files", result.Files,
		"directories", result.Directories,
		"symlinks", result.Symlinks,
		"bytes", result.Bytes,
		"cloned_bytes", result.ClonedBytes,
		"copied_bytes", result.CopiedBytes,
		"previous_generation", currentGeneration,
	)
	return true, nil
}

func scanSeedSource(ctx context.Context, sourceRoot string) (seedManifest, error) {
	manifest := seedManifest{
		Format: seedFormatVersion, SourceRoot: filepath.Clean(sourceRoot), Entries: make(map[string]seedEntry),
	}
	var files, bytes int64
	err := sandbox.SurveyWalk(ctx, sourceRoot, sandbox.SurveyOptions{IncludeHidden: true},
		func(entry sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
			info, err := entry.DirEntry.Info()
			if err != nil {
				if sourceChangeSkippable(err) {
					return sandbox.SurveyContinue, nil
				}
				return sandbox.SurveyContinue, err
			}
			fingerprint := seedEntry{
				Size: info.Size(), Mode: uint32(info.Mode()), ModTime: info.ModTime().UnixNano(),
				Identity: sourceFileIdentity(info),
			}
			switch {
			case entry.IsDir:
				fingerprint.Kind = "directory"
			case entry.IsSymlink:
				fingerprint.Kind = "symlink"
				target, err := os.Readlink(entry.Abs)
				if err != nil {
					if sourceChangeSkippable(err) {
						return sandbox.SurveyContinue, nil
					}
					return sandbox.SurveyContinue, err
				}
				fingerprint.LinkTarget = target
			case info.Mode().IsRegular():
				fingerprint.Kind = "regular"
				files++
				bytes += max(info.Size(), 0)
			default:
				fingerprint.Kind = "unsupported"
			}
			manifest.Entries[entry.Rel] = fingerprint
			reportPreparation(ctx, PreparationProgress{
				Strategy: ProvisionBridgeCoW, Stage: "surveying_source", Files: files, Bytes: bytes,
			})
			return sandbox.SurveyContinue, nil
		})
	return manifest, err
}

type seedEvictionCandidate struct {
	key      string
	path     string
	lastUsed time.Time
}

func (m *Manager) ensureSeedCapacity(ctx context.Context, required uint64, excludeKey string) error {
	if err := m.evictExpiredSeeds(ctx, excludeKey); err != nil {
		slog.WarnContext(ctx, "evict expired worker seeds", "err", err)
	}
	available, known, err := queryAvailableStorageBytes(m.seedRoot)
	if err != nil {
		return err
	}
	if !known || available >= required {
		return nil
	}
	candidates, err := seedEvictionCandidates(m.seedRoot, excludeKey)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if err := m.tryEvictSeed(ctx, candidate); err != nil {
			slog.WarnContext(ctx, "evict worker seed under storage pressure", "seed", candidate.key, "err", err)
			continue
		}
		available, known, err = queryAvailableStorageBytes(m.seedRoot)
		if err != nil {
			return err
		}
		if !known || available >= required {
			return nil
		}
	}
	return &CapacityError{Path: m.seedRoot, Required: required, Available: available}
}

func (m *Manager) evictExpiredSeeds(ctx context.Context, excludeKey string) error {
	candidates, err := seedEvictionCandidates(m.seedRoot, excludeKey)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-seedRetention)
	for _, candidate := range candidates {
		if candidate.lastUsed.After(cutoff) {
			continue
		}
		if err := m.tryEvictSeed(ctx, candidate); err != nil {
			slog.DebugContext(ctx, "skip active expired worker seed", "seed", candidate.key, "err", err)
		}
	}
	return nil
}

func seedEvictionCandidates(seedRoot, excludeKey string) ([]seedEvictionCandidate, error) {
	entries, err := os.ReadDir(seedRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	candidates := make([]seedEvictionCandidate, 0, len(entries))
	for _, entry := range entries {
		key := entry.Name()
		if !entry.IsDir() || key == excludeKey || !validSeedKey(key) {
			continue
		}
		path := filepath.Join(seedRoot, key)
		info, err := os.Stat(filepath.Join(path, seedLastUsedFile))
		if err != nil {
			info, err = entry.Info()
		}
		if err != nil {
			continue
		}
		candidates = append(candidates, seedEvictionCandidate{key: key, path: path, lastUsed: info.ModTime()})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].lastUsed.Before(candidates[j].lastUsed) })
	return candidates, nil
}

func validSeedKey(key string) bool {
	if len(key) != 16 {
		return false
	}
	for _, char := range key {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}

func (m *Manager) tryEvictSeed(ctx context.Context, candidate seedEvictionCandidate) error {
	inProcess := m.locks.forKey(candidate.key)
	if !inProcess.TryLock() {
		return fmt.Errorf("seed is active")
	}
	defer inProcess.Unlock()
	lock, err := filelock.Open(filepath.Join(m.seedRoot, seedLocksDir, candidate.key+".lock"))
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	locked, err := filelock.TryExclusive(lock)
	if err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("seed is active")
	}
	defer func() { _ = filelock.Unlock(lock) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.RemoveAll(candidate.path)
}

func touchSeedLastUsed(seedDir string) {
	now := time.Now()
	path := filepath.Join(seedDir, seedLastUsedFile)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		_ = os.WriteFile(path, nil, 0o600)
	}
	_ = os.Chtimes(path, now, now)
}

func loadCurrentSeed(seedDir string) (generation, tree string, manifest seedManifest) {
	raw, err := os.ReadFile(filepath.Join(seedDir, seedCurrentFile))
	if err != nil {
		return "", "", seedManifest{}
	}
	generation = strings.TrimSpace(string(raw))
	if _, err := uuid.Parse(generation); err != nil {
		return "", "", seedManifest{}
	}
	dir := filepath.Join(seedDir, seedGenerations, generation)
	manifestRaw, err := os.ReadFile(filepath.Join(dir, seedManifestFile)) // #nosec G703 -- generation is a validated UUID under seedDir
	if err != nil || json.Unmarshal(manifestRaw, &manifest) != nil || manifest.Format != seedFormatVersion {
		return "", "", seedManifest{}
	}
	tree = filepath.Join(dir, "tree")
	if info, err := os.Stat(tree); err != nil || !info.IsDir() { // #nosec G703 -- tree is under the validated UUID generation
		return "", "", seedManifest{}
	}
	return generation, tree, manifest
}

func manifestsEqual(left, right seedManifest) bool {
	if left.Format != right.Format || left.SourceRoot != right.SourceRoot || len(left.Entries) != len(right.Entries) {
		return false
	}
	for path, entry := range left.Entries {
		if right.Entries[path] != entry {
			return false
		}
	}
	return true
}

func writeSeedManifest(path string, manifest seedManifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encodeErr := json.NewEncoder(file).Encode(manifest)
	closeErr := file.Close()
	if encodeErr != nil {
		return encodeErr
	}
	return closeErr
}

func publishCurrentGeneration(seedDir, generation string) error {
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: seedDir, Rel: seedCurrentFile},
		Source:   strings.NewReader(generation + "\n"),
		Mode:     0o600,
		DirMode:  0o700,
	})
	return err
}

func cleanupSeedGenerations(seedDir, retain string) {
	dir := filepath.Join(seedDir, seedGenerations)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Name() == retain {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
	}
}

// RemoveSeed removes only rebuildable host state for sourceRoot.
func RemoveSeed(ctx context.Context, seedRoot, sourceRoot string) error {
	if strings.TrimSpace(seedRoot) == "" || strings.TrimSpace(sourceRoot) == "" {
		return nil
	}
	target := enginepaths.ProjectSeedDir(seedRoot, sourceRoot)
	rel, err := filepath.Rel(filepath.Clean(seedRoot), filepath.Clean(target))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("refusing worker seed removal outside seed root")
	}
	if _, err := os.Stat(target); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	release, err := lockSeedAcrossProcesses(ctx, seedRoot, enginepaths.ProjectKey(sourceRoot), true)
	if err != nil {
		return err
	}
	defer release()
	if err := os.RemoveAll(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ListSeedCaches inventories valid published bridge caches.
func ListSeedCaches(ctx context.Context, seedRoot string) ([]SeedCacheStatus, error) {
	if strings.TrimSpace(seedRoot) == "" {
		return nil, nil
	}
	candidates, err := seedEvictionCandidates(seedRoot, "")
	if err != nil {
		return nil, err
	}
	statuses := make([]SeedCacheStatus, 0, len(candidates))
	for _, candidate := range candidates {
		_, tree, manifest := loadCurrentSeed(candidate.path)
		if tree == "" || strings.TrimSpace(manifest.SourceRoot) == "" {
			continue
		}
		allocated, err := treeAllocatedBytes(ctx, candidate.path)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, SeedCacheStatus{
			ID:             candidate.key,
			SourceRoot:     manifest.SourceRoot,
			LogicalBytes:   regularBytes(manifest),
			AllocatedBytes: allocated,
			LastUsed:       candidate.lastUsed,
		})
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].LastUsed.After(statuses[j].LastUsed) })
	return statuses, nil
}

// RemoveSeedByID removes one rebuildable bridge cache under an exclusive lease.
func RemoveSeedByID(ctx context.Context, seedRoot, id string) error {
	if strings.TrimSpace(seedRoot) == "" {
		return fmt.Errorf("worker cache storage is unavailable")
	}
	if !validSeedKey(id) {
		return fmt.Errorf("invalid worker cache id")
	}
	release, err := lockSeedAcrossProcesses(ctx, seedRoot, id, true)
	if err != nil {
		return err
	}
	defer release()
	return os.RemoveAll(filepath.Join(seedRoot, id))
}

func treeAllocatedBytes(ctx context.Context, root string) (int64, error) {
	var total int64
	err := sandbox.SurveyWalk(ctx, root, sandbox.SurveyOptions{IncludeHidden: true},
		func(entry sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
			if entry.IsDir {
				return sandbox.SurveyContinue, nil
			}
			info, err := entry.DirEntry.Info()
			if err != nil {
				return sandbox.SurveyContinue, err
			}
			total += max(allocatedFileBytes(info), 0)
			return sandbox.SurveyContinue, nil
		})
	return total, err
}
