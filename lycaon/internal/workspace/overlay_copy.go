package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/claim"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// CreateWorkerWorkspace creates a complete single-root branch.
func (m *Manager) CreateWorkerWorkspace(ctx context.Context, primaryDir, jobID string) (*Binding, error) {
	primary, err := absDir(primaryDir)
	if err != nil {
		return nil, err
	}
	roots := []projectroot.RootRef{{ID: "primary", Path: primary, IsPrimary: true}}
	binding, _, err := m.CreateWorkerWorkspaceFromSources(ctx, roots, roots, "primary", jobID)
	return binding, err
}

// DestroyWorkerWorkspace releases a branch's views, proves no mount remains in it, drops its retention
// and deletes it. Any failure keeps the branch and its metadata: the view is still in custody, and
// recursive removal through a live mount would delete the worker's result.
func (m *Manager) DestroyWorkerWorkspace(binding *Binding) error {
	if binding == nil || strings.TrimSpace(binding.Root) == "" {
		return nil
	}
	root := strings.TrimSpace(binding.Root)
	jobID := jobIDForBranch(root)
	if err := m.releaseBranchRuntime(binding, root, jobID); err != nil {
		return err
	}
	if err := RefuseMountedBranch(root); err != nil {
		return err
	}
	if err := m.releaseBranchRetention(jobID); err != nil {
		return err
	}
	releaseBranchClaimState(root)
	err := errors.Join(os.RemoveAll(enginepaths.MetaDirForBranchRoot(root)), os.RemoveAll(root))
	removeBranchLocks(root)
	return err
}

func (m *Manager) releaseBranchRuntime(binding *Binding, root, jobID string) error {
	mounted, err := m.settleMountedBranch(root, jobID)
	if err != nil {
		return err
	}
	if mounted {
		m.forgetBranchViews(root)
		return nil
	}
	settled, err := m.releaseBranchViews(root)
	if err != nil {
		return err
	}
	if binding.Adapter == nil || binding.View == nil || settled[binding.View.ViewID] {
		return nil
	}
	return releaseAdapterView(binding.Adapter, binding.View.ViewID)
}

// releaseBranchRetention drops what recovery retained for a worker whose branch is going away, for a
// result promoted or discarded. Collection removes only what nothing else still references.
func (m *Manager) releaseBranchRetention(jobID string) error {
	if strings.TrimSpace(m.branchRoot) == "" {
		return nil
	}
	services, err := m.layered()
	if err != nil {
		return err
	}
	return services.recovery.ReleaseWorkerResult(jobID)
}

// settleMountedBranch releases the branch's mounted view when it has one. On failure the caller must
// keep the branch and its metadata: the view is still in custody, and the directory may still be a mount.
func (m *Manager) settleMountedBranch(branchRoot, jobID string) (bool, error) {
	branchClaimsMu.RLock()
	rec, ok := branchClaimStates[filepath.Clean(branchRoot)]
	branchClaimsMu.RUnlock()
	if ok && rec.layered != nil {
		return true, settleBranchLayeredView(context.Background(), branchRoot, rec.layered)
	}
	if strings.TrimSpace(m.branchRoot) == "" {
		return false, nil
	}
	services, err := m.layered()
	if err != nil {
		return false, err
	}
	if !services.coordinator.SettlementUnresolved(jobID) {
		return false, nil
	}
	return true, services.coordinator.ReleaseWorkerView(context.Background(), jobID, services.retention)
}

func markSnapshotCompleteWithClaim(branchRoot string, choice WorkspaceStrategySelection, view *adapter.PreparedView, cv *claim.ClaimedView, hints []string) error {
	metaDir := enginepaths.MetaDirForBranchRoot(branchRoot)
	meta, err := LoadJobMeta(metaDir)
	if err != nil {
		return err
	}
	cpFacts := choice.Facts
	if view != nil && view.Facts.AdapterID != "" {
		cpFacts = view.Facts
	}
	meta.FormatVersion, meta.SnapshotComplete = JobMetaFormatV2, true
	meta.AdapterID, meta.PreparationKind = choice.AdapterID, choice.Facts.PreparationKind
	meta.VisibilityMode, meta.FallbackReason = choice.Facts.UntouchedSourceVisibility, choice.Facts.FallbackReason
	meta.SelectionReason, meta.DeclaredCost = choice.Selection.Reason, choice.Selection.DeclaredCost
	meta.PendingGitInit = choice.Facts.PreparationKind == adapter.PreparationMounted
	meta.SourceGeneration, meta.FocusPathHints, meta.AdapterFacts = 1, append([]string(nil), hints...), &cpFacts
	if view != nil {
		meta.ViewID, meta.PreparedViewID = view.ViewID, view.ViewID
		meta.CopiedEntries, meta.CopiedBytes = view.Materialize.Entries, view.Materialize.CopiedBytes
		if view.BackingDir != "" {
			meta.JournalPath = filepath.Join(view.BackingDir, "journal")
		}
	}
	if cv != nil {
		meta.JournalPath = cv.Metadata.JournalPath
	} else {
		recordBranchClaimState(branchRoot, nil, snapshotFromJobMeta(branchRoot, meta), nil)
	}
	return WriteJobMeta(metaDir, meta)
}

func snapshotFromJobMeta(branchRoot string, meta JobMeta) claim.WorkspaceStateSnapshot {
	roots := make([]claim.RootMappingRecord, 0, len(meta.Roots))
	for _, r := range meta.Roots {
		roots = append(roots, claim.RootMappingRecord{
			RootID: r.ID, Label: r.Label, SourcePath: r.Path, WorkspacePath: branchRoot, IsPrimary: r.IsPrimary,
		})
	}
	gitState := claim.GitInitInitialized
	if meta.PendingGitInit {
		gitState = claim.GitInitLazyPending
	}
	notice := claim.FallbackNotice{Severity: claim.SeverityInfo}
	if meta.FallbackReason != "" {
		notice = claim.FallbackNotice{
			Severity: claim.SeverityWarning, ReasonCode: meta.FallbackReason,
			CopiedEntries: meta.CopiedEntries, CopiedBytes: meta.CopiedBytes,
		}
	}
	return claim.WorkspaceStateSnapshot{
		Identity:       claim.ClaimIdentity{WorkerID: jobIDForBranch(branchRoot), ViewID: meta.ViewID, SourceGeneration: uint64(max(meta.SourceGeneration, 0))},
		Readiness:      claim.StateReady,
		Adapter:        claim.AdapterMetadataFacts{AdapterID: meta.AdapterID, PreparationKind: meta.PreparationKind, SourceVisibility: meta.VisibilityMode, FallbackReason: meta.FallbackReason, CopiedEntries: meta.CopiedEntries, CopiedBytes: meta.CopiedBytes, PendingGitInit: meta.PendingGitInit},
		SelectedRoots:  roots,
		FocusPathHints: append([]string(nil), meta.FocusPathHints...),
		Notice:         notice,
		GitInit:        gitState,
		Command:        claim.CommandIdle,
		Checkpoint:     claim.CheckpointStatus{State: claim.CheckpointNone},
	}
}

func jobRoot(m *Manager, primaryDir, jobID string) string {
	return enginepaths.JobBranchDir(m.branchRoot, primaryDir, jobID)
}
