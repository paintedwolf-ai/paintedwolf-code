package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/enginepaths"
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

func provisionRoot(ctx context.Context, m *Manager, sourceRoot, branchRoot string) error {
	maxEntries, maxBytes := effectiveLimits(ctx, m)
	if est := CachedSeedEstimate(m.seedRoot, sourceRoot); est != nil {
		if err := checkPreparationLimits(est.EstimatedEntries, est.EstimatedBytes, maxEntries, maxBytes, true); err != nil {
			_ = os.RemoveAll(branchRoot)
			return err
		}
	}
	reportPreparation(ctx, PreparationProgress{Stage: "probing"})
	strategy, err := chooseProvisionStrategy(ctx, m, sourceRoot, branchRoot)
	if err != nil {
		return classifyProvisionError(err)
	}
	base := PreparationProgress{Strategy: strategy}
	if strings.TrimSpace(m.seedRoot) != "" {
		if err := evictExpiredSeeds(ctx, m, enginepaths.ProjectKey(sourceRoot)); err != nil {
			slog.WarnContext(ctx, "evict expired worker caches", "err", err)
		}
	}
	if strategy != ProvisionBridgeCoW {
		base.Stage = "snapshotting_branch"
		reportPreparation(ctx, base)
		result, mirrorErr := mirrorTree(ctx, sourceRoot, branchRoot, mirrorOptions{
			phase: "worker_branch_" + string(strategy), tolerateChange: true, preparation: base, maxEntries: maxEntries, maxBytes: maxBytes,
		})
		if mirrorErr != nil {
			_ = os.RemoveAll(branchRoot)
			return classifyProvisionError(fmt.Errorf("snapshot worker branch: %w", markIncompleteSeedCleaned(mirrorErr)))
		}
		if err := validateCompleteMirror(result); err != nil {
			_ = os.RemoveAll(branchRoot)
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
	refreshed, err := refreshSeedLocked(ctx, m, sourceRoot, base)
	if err != nil {
		_ = os.RemoveAll(branchRoot)
		return classifyProvisionError(fmt.Errorf("refresh worker seed: %w", markIncompleteSeedCleaned(err)))
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
		phase: "worker_branch_bridge_cow", preparation: base, maxEntries: maxEntries, maxBytes: maxBytes,
	})
	if err != nil {
		_ = os.RemoveAll(branchRoot)
		return classifyProvisionError(fmt.Errorf("snapshot worker branch: %w", markIncompleteSeedCleaned(err)))
	}
	touchSeedLastUsed(seedDir)
	logProvisionResult(ctx, sourceRoot, strategy, refreshed, result)
	return nil
}

func logProvisionResult(ctx context.Context, sourceRoot string, strategy ProvisionStrategy, refreshed bool, result mirrorResult) {
	slog.InfoContext(ctx, "prepared isolated worker workspace",
		"source", sourceRoot, "strategy", strategy, "seed_refreshed", refreshed,
		"files", result.Files, "bytes", result.Bytes, "cloned_files", result.ClonedFiles,
		"cloned_bytes", result.ClonedBytes, "copied_bytes", result.CopiedBytes,
	)
}

func refreshSeedLocked(ctx context.Context, m *Manager, sourceRoot string, progress PreparationProgress) (bool, error) {
	key := enginepaths.ProjectKey(sourceRoot)
	inProcess := m.locks.forKey(key)
	inProcess.Lock()
	defer inProcess.Unlock()
	release, err := lockSeedAcrossProcesses(ctx, m.seedRoot, key, true)
	if err != nil {
		return false, err
	}
	defer release()
	return refreshSeed(ctx, m, sourceRoot, progress)
}

func refreshSeed(ctx context.Context, m *Manager, sourceRoot string, progress PreparationProgress) (bool, error) {
	cleanRoot, err := absDir(sourceRoot)
	if err != nil {
		return false, err
	}
	sourceRoot = cleanRoot
	seedDir := enginepaths.ProjectSeedDir(m.seedRoot, sourceRoot)
	if err := os.MkdirAll(filepath.Join(seedDir, seedGenerations), 0o700); err != nil {
		return false, err
	}

	maxEntries, maxBytes := effectiveLimits(ctx, m)
	manifest, err := scanSeedSource(ctx, sourceRoot, maxEntries, maxBytes)
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
		phase: "seed_refresh", tolerateChange: true, preparation: progress, maxEntries: maxEntries, maxBytes: maxBytes,
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
		return false, markIncompleteSeedCleaned(err)
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
	return true, nil
}

func scanSeedSource(ctx context.Context, sourceRoot string, maxEntries, maxBytes int64) (seedManifest, error) {
	manifest := seedManifest{
		Format: seedFormatVersion, SourceRoot: filepath.Clean(sourceRoot), Entries: make(map[string]seedEntry),
	}
	var entries, files, bytes int64
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
			entries++
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
			if limitErr := checkPreparationLimits(entries, bytes, maxEntries, maxBytes, true); limitErr != nil {
				return sandbox.SurveyStop, limitErr
			}
			manifest.Entries[entry.Rel] = fingerprint
			reportPreparation(ctx, PreparationProgress{
				Strategy: ProvisionBridgeCoW, Stage: "surveying_source", Files: files, Bytes: bytes,
			})
			return sandbox.SurveyContinue, nil
		})
	return manifest, err
}
