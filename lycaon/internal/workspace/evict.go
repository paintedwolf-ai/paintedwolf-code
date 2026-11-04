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
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
)

// EvictVerifiedCheckpointBranch evicts a worker branch tree and disposable working material only
// when no reader holds the branch and a complete, verified checkpoint in coord can rebuild it.
func EvictVerifiedCheckpointBranch(ctx context.Context, branchRoot string, coord *recovery.Coordinator, workerID, viewID, backingDir string) (*recovery.EvictionReport, error) {
	if coord == nil {
		return nil, fmt.Errorf("evict checkpoint branch: coordinator is required")
	}
	rep, err := coord.EvictDisposableWorkingMaterial(ctx, workerID, viewID, backingDir)
	if err != nil {
		return nil, err
	}
	return rep, EvictJobTree(ctx, branchRoot)
}

// RebuildWorkerWorkspaceFromCheckpoint recreates an evicted branch tree at branchRoot and
// reconstructs its state from the worker's last validated published checkpoint.
func (m *Manager) RebuildWorkerWorkspaceFromCheckpoint(ctx context.Context, branchRoot string, coord *recovery.Coordinator, req recovery.RecoverRequest) (*recovery.RecoveryResult, SandboxLayout, error) {
	if coord == nil {
		return nil, SandboxLayout{}, fmt.Errorf("rebuild checkpoint branch: coordinator is required")
	}
	var recRes *recovery.RecoveryResult
	layout, err := m.RebuildWorkerWorkspace(ctx, branchRoot, func(rCtx context.Context, layout SandboxLayout, root string) (rErr error) {
		if req.PrepareReq.TargetRootDirs == nil {
			req.PrepareReq.TargetRootDirs = make(map[string]string, len(layout.Roots))
		}
		for _, r := range layout.Roots {
			if r.IsPrimary && req.PrepareReq.TargetRootDirs[r.ID] == "" {
				req.PrepareReq.TargetRootDirs[r.ID] = root
			}
		}
		recRes, rErr = coord.RecoverWorkerView(rCtx, req)
		return rErr
	})
	return recRes, layout, err
}

// EvictJobTree removes a branch tree while keeping its .meta sibling, so the
// tree can be rebuilt from the job's baseline and overlay records. It refuses
// while any reader holds the branch. The meta is marked incomplete before the
// first byte is removed, so an interrupted eviction reads as an evicted tree.
func EvictJobTree(ctx context.Context, branchRoot string) error {
	branchRoot, err := cleanBranchRoot(branchRoot)
	if err != nil {
		return err
	}
	releaseUse, err := tryExclusiveBranchUse(branchRoot)
	if err != nil {
		return err
	}
	defer releaseUse()
	releaseProvision, err := acquireWorkspaceProvisionLock(ctx, branchRoot)
	if err != nil {
		return err
	}
	defer releaseProvision()
	metaDir := enginepaths.MetaDirForBranchRoot(branchRoot)
	meta, err := LoadJobMeta(metaDir)
	if err != nil {
		return fmt.Errorf("evict worker branch: %w", err)
	}
	if meta.SnapshotComplete {
		meta.SnapshotComplete = false
		if err := WriteJobMeta(metaDir, meta); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(branchRoot); err != nil {
		return fmt.Errorf("evict worker branch: %w", err)
	}
	return nil
}

// RebuildWorkerWorkspace recreates an evicted branch tree at its recorded path.
// restore fills the fresh skeleton from the job's records; the layout is the
// one captured at claim time. A tree that is already complete is left alone.
func (m *Manager) RebuildWorkerWorkspace(ctx context.Context, branchRoot string, restore func(ctx context.Context, layout SandboxLayout, branchRoot string) error) (SandboxLayout, error) {
	branchRoot, err := cleanBranchRoot(branchRoot)
	if err != nil {
		return SandboxLayout{}, err
	}
	release, err := acquireWorkspaceProvisionLock(ctx, branchRoot)
	if err != nil {
		return SandboxLayout{}, err
	}
	defer release()
	if layout, err := LoadBranchLayout(branchRoot); err == nil {
		return layout, nil
	}
	meta, err := LoadJobMeta(enginepaths.MetaDirForBranchRoot(branchRoot))
	if err != nil {
		return SandboxLayout{}, fmt.Errorf("rebuild worker branch: %w", err)
	}
	layout, err := layoutFromJobMeta(meta)
	if err != nil {
		return SandboxLayout{}, err
	}
	binding, err := m.createBranchSkeleton(ctx, branchRoot, layout, jobIDForBranch(branchRoot))
	if err != nil {
		return SandboxLayout{}, err
	}
	if err := restore(ctx, layout, binding.Root); err != nil {
		_ = os.RemoveAll(binding.Root)
		return SandboxLayout{}, err
	}
	if err := markSnapshotComplete(enginepaths.MetaDirForBranchRoot(binding.Root)); err != nil {
		return SandboxLayout{}, err
	}
	TouchBranchUse(binding.Root)
	return layout, nil
}

type seedEvictionCandidate struct {
	key, path string
	lastUsed  time.Time
}

func (m *Manager) ensureSeedCapacity(ctx context.Context, required uint64, excludeKey string) error {
	if err := evictExpiredSeeds(ctx, m, excludeKey); err != nil {
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
		if err := tryEvictSeed(ctx, m, candidate); err != nil {
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

func evictExpiredSeeds(ctx context.Context, m *Manager, excludeKey string) error {
	candidates, err := seedEvictionCandidates(m.seedRoot, excludeKey)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-seedRetention)
	for _, candidate := range candidates {
		if candidate.lastUsed.After(cutoff) {
			continue
		}
		if err := tryEvictSeed(ctx, m, candidate); err != nil {
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

func tryEvictSeed(ctx context.Context, m *Manager, candidate seedEvictionCandidate) error {
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
