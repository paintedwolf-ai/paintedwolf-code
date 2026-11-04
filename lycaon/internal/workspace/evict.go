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
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// ErrBranchRetained reports a branch that holds the only copy of a result or has unsettled
// runtime. Retention steps aside; it is not a sweep failure.
var ErrBranchRetained = errors.New("worker branch is retained")

// EvictWorkerBranch is the retention path. A worker with a recovery journal evicts only from a
// complete verified checkpoint; any other tree evicts on its sealed overlay record.
func (m *Manager) EvictWorkerBranch(ctx context.Context, branchRoot string) error {
	jobID := jobIDForBranch(branchRoot)
	if m.recovery == nil {
		return EvictJobTree(ctx, branchRoot)
	}
	journal, err := m.recovery.LoadWorkerJournal(jobID)
	if errors.Is(err, os.ErrNotExist) {
		return EvictJobTree(ctx, branchRoot)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBranchRetained, err)
	}
	idents, err := m.recovery.ListMountIdentities(jobID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBranchRetained, err)
	}
	if len(idents) > 0 {
		return fmt.Errorf("%w: %d recorded views of %s have not settled", ErrBranchRetained, len(idents), jobID)
	}
	if _, err := EvictVerifiedCheckpointBranch(ctx, branchRoot, m.recovery, jobID, journal.ViewID, journal.BackingDir); err != nil {
		if errors.Is(err, recovery.ErrEvictUnsealedWorkRefused) {
			return fmt.Errorf("%w: %w", ErrBranchRetained, err)
		}
		return err
	}
	return nil
}

// EvictVerifiedCheckpointBranch evicts a worker branch tree and disposable working material only
// when no reader holds the branch and a complete, verified checkpoint in coord can rebuild it.
// The lease is taken first, so a refusal leaves a reader's working material untouched.
func EvictVerifiedCheckpointBranch(ctx context.Context, branchRoot string, coord *recovery.Coordinator, workerID, viewID, backingDir string) (*recovery.EvictionReport, error) {
	if coord == nil {
		return nil, fmt.Errorf("evict checkpoint branch: coordinator is required")
	}
	branchRoot, err := cleanBranchRoot(branchRoot)
	if err != nil {
		return nil, err
	}
	releaseUse, err := tryExclusiveBranchUse(branchRoot)
	if err != nil {
		return nil, err
	}
	defer releaseUse()
	if err := RefuseMountedBranch(branchRoot); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBranchRetained, err)
	}
	rep, err := coord.EvictDisposableWorkingMaterial(ctx, workerID, viewID, backingDir)
	if err != nil {
		return nil, err
	}
	return rep, removeEvictedTree(ctx, branchRoot)
}

// RebuildWorkerWorkspaceFromCheckpoint recreates an evicted branch tree at branchRoot and
// reconstructs its state from the worker's last validated published checkpoint.
func (m *Manager) RebuildWorkerWorkspaceFromCheckpoint(ctx context.Context, branchRoot string, coord *recovery.Coordinator, req recovery.RecoverRequest) (*recovery.RecoveryResult, SandboxLayout, error) {
	if coord == nil {
		return nil, SandboxLayout{}, fmt.Errorf("rebuild checkpoint branch: coordinator is required")
	}
	if err := RefuseMountedBranch(branchRoot); err != nil {
		return nil, SandboxLayout{}, err
	}
	var recRes *recovery.RecoveryResult
	layout, err := m.RebuildWorkerWorkspace(ctx, branchRoot, func(rCtx context.Context, layout SandboxLayout, root string) (rErr error) {
		if err := fillRecoveryTargets(&req.PrepareReq, layout, root); err != nil {
			return err
		}
		recRes, rErr = coord.RecoverWorkerView(rCtx, req)
		return rErr
	})
	return recRes, layout, err
}

// fillRecoveryTargets maps each root to its place in the branch, as the original claim did.
func fillRecoveryTargets(req *adapter.PrepareRequest, layout SandboxLayout, root string) error {
	if req.TargetRootDirs == nil {
		req.TargetRootDirs = make(map[string]string, len(layout.Roots))
	}
	for _, r := range layout.Roots {
		if req.TargetRootDirs[r.ID] != "" {
			continue
		}
		if len(layout.Roots) == 1 {
			req.TargetRootDirs[r.ID] = root
			continue
		}
		dir, err := projectroot.BranchDirForID(r.ID)
		if err != nil {
			return err
		}
		req.TargetRootDirs[r.ID] = filepath.Join(root, dir)
	}
	if len(req.Grants) == 0 {
		for _, r := range layout.Roots {
			label := r.Label
			if label == "" {
				label = r.ID
			}
			req.Grants = append(req.Grants, adapter.RootGrant{RootID: r.ID, Label: label, SourcePath: r.Path, IsPrimary: r.IsPrimary})
		}
	}
	return nil
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
	return removeEvictedTree(ctx, branchRoot)
}

// removeEvictedTree requires the caller's exclusive use lease.
func removeEvictedTree(ctx context.Context, branchRoot string) error {
	releaseProvision, err := acquireWorkspaceProvisionLock(ctx, branchRoot)
	if err != nil {
		return err
	}
	defer releaseProvision()
	if err := RefuseMountedBranch(branchRoot); err != nil {
		return fmt.Errorf("%w: %w", ErrBranchRetained, err)
	}
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
