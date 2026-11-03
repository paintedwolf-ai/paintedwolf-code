package claim

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

// Coordinator orchestrates O(selected roots) worker view claiming, retention registration,
// confinement verification, Format-v2 metadata persistence, and typed workspace state (#1662).
type Coordinator struct {
	selector        *adapter.Selector
	mu              sync.RWMutex
	views           map[string]*ClaimedView
	failedSnapshots map[string]WorkspaceStateSnapshot
}

// NewCoordinator constructs a claim Coordinator backed by an adapter Selector.
func NewCoordinator(sel *adapter.Selector) *Coordinator {
	if sel == nil {
		sel = adapter.NewSelector("", nil, nil, adapter.NewEagerCopyAdapter(adapter.ReasonNone))
	}
	return &Coordinator{
		selector:        sel,
		views:           make(map[string]*ClaimedView),
		failedSnapshots: make(map[string]WorkspaceStateSnapshot),
	}
}

// ClaimWorkerView validates attached-root identities and explicit WorkspaceSelection, calls
// Adapter.Prepare once, registers retention before readiness, verifies confinement usability,
// commits Format-v2 metadata, and exposes the ready view (#1662).
func (c *Coordinator) ClaimWorkerView(
	ctx context.Context,
	req ClaimRequest,
) (*ClaimedView, WorkspaceStateSnapshot, error) {
	if strings.TrimSpace(req.Identity.WorkerID) == "" || strings.TrimSpace(req.Identity.ViewID) == "" {
		return nil, WorkspaceStateSnapshot{}, fmt.Errorf("%w: WorkerID and ViewID are required", ErrInvalidMetadata)
	}
	if strings.TrimSpace(req.Storage.RuntimeDir) == "" {
		return nil, WorkspaceStateSnapshot{}, fmt.Errorf("%w: Storage.RuntimeDir is required", ErrInvalidMetadata)
	}

	c.mu.RLock()
	if existing, ok := c.views[req.Identity.ViewID]; ok && existing.State.Readiness == StateReady {
		snap := existing.State
		c.mu.RUnlock()
		return existing, snap, nil
	}
	c.mu.RUnlock()

	if err := checkStageBoundary(ctx, req.Hooks.Fault, StageValidateSelection); err != nil {
		snap := c.recordFailedClaim(req, adapter.AdapterEagerCopy, classifyClaimReason(err), err.Error(), 0, 0)
		return nil, snap, err
	}
	if _, err := adapter.ValidateWorkspaceSelection(req.Grants, req.Selection); err != nil {
		snap := c.recordFailedClaim(req, adapter.AdapterEagerCopy, classifyClaimReason(err), err.Error(), 0, 0)
		return nil, snap, err
	}

	adp, probeRes, err := c.selector.Select(ctx, adapter.ProbeConfig{RuntimeDir: req.Storage.RuntimeDir})
	if err != nil {
		snap := c.recordFailedClaim(req, probeRes.AdapterID, classifyClaimReason(err), err.Error(), 0, 0)
		return nil, snap, err
	}

	if err := checkStageBoundary(ctx, req.Hooks.Fault, StagePrepareAdapter); err != nil {
		snap := c.recordFailedClaim(req, adp.ID(), classifyClaimReason(err), err.Error(), 0, 0)
		return nil, snap, err
	}

	prepReq := buildPrepareRequest(req)
	prepared, err := adp.Prepare(ctx, prepReq)
	if err != nil {
		reason, diag, entries, bytes := classifyPrepareError(err, probeRes.ReasonCode)
		snap := c.recordFailedClaim(req, adp.ID(), reason, diag, entries, bytes)
		return nil, snap, err
	}

	var retentionClaimID string
	metaPath := resolveMetadataPath(req.Storage, prepared.RuntimeViewDir)
	rollback := func() {
		if retentionClaimID != "" && req.Hooks.Retention != nil {
			_ = req.Hooks.Retention.ReleaseClaim(retentionClaimID)
		}
		if req.Hooks.PrivateGit != nil {
			_ = req.Hooks.PrivateGit.ReleaseView(req.Identity.ViewID)
		}
		_, _ = adp.Release(context.Background(), req.Identity.ViewID)
		_ = os.Remove(metaPath)
	}

	if req.Hooks.PrivateGit != nil && adp.Facts().PreparationKind == adapter.PreparationMounted {
		if bErr := bindMountedPrivateGitLazy(req, adp.ID(), prepared); bErr != nil {
			rollback()
			snap := c.recordFailedClaim(req, adp.ID(), adapter.ReasonMountDenied, bErr.Error(), 0, 0)
			return nil, snap, bErr
		}
	}

	if err := checkStageBoundary(ctx, req.Hooks.Fault, StageRegisterRetention); err != nil {
		rollback()
		snap := c.recordFailedClaim(req, adp.ID(), classifyClaimReason(err), err.Error(), 0, 0)
		return nil, snap, err
	}
	if req.Hooks.Retention != nil {
		cid, rErr := req.Hooks.Retention.RegisterClaim(ctx, req.Identity.WorkerID, req.Identity.ViewID, collectPreparedRootPaths(prepared.Roots))
		if rErr != nil {
			rollback()
			wrapped := fmt.Errorf("%w: %v", ErrRetentionFailed, rErr)
			snap := c.recordFailedClaim(req, adp.ID(), adapter.ReasonMountDenied, wrapped.Error(), 0, 0)
			return nil, snap, wrapped
		}
		retentionClaimID = cid
	}

	if err := checkStageBoundary(ctx, req.Hooks.Fault, StageVerifyConfinement); err != nil {
		rollback()
		snap := c.recordFailedClaim(req, adp.ID(), classifyClaimReason(err), err.Error(), 0, 0)
		return nil, snap, err
	}
	if err := verifyPreparedConfinement(ctx, adp, prepared, req.Hooks.VerifyConfinement); err != nil {
		rollback()
		snap := c.recordFailedClaim(req, adp.ID(), adapter.ReasonBackingConfinementFailed, err.Error(), 0, 0)
		return nil, snap, err
	}

	if err := checkStageBoundary(ctx, req.Hooks.Fault, StagePersistMetadata); err != nil {
		rollback()
		snap := c.recordFailedClaim(req, adp.ID(), classifyClaimReason(err), err.Error(), 0, 0)
		return nil, snap, err
	}

	meta := buildCommittedMetadataV2(req, adp, prepared, probeRes.ReasonCode)
	if err := SaveViewMetadata(metaPath, meta); err != nil {
		rollback()
		snap := c.recordFailedClaim(req, adp.ID(), adapter.ReasonMountDenied, err.Error(), 0, 0)
		return nil, snap, err
	}

	readySnap := buildReadySnapshot(meta)
	claimed := &ClaimedView{
		Identity:         req.Identity,
		Adapter:          adp,
		Prepared:         prepared,
		Metadata:         meta,
		RetentionClaimID: retentionClaimID,
		State:            readySnap,
	}
	c.selector.Registry.Register(prepared, adp)
	c.mu.Lock()
	delete(c.failedSnapshots, req.Identity.ViewID)
	c.views[req.Identity.ViewID] = claimed
	c.mu.Unlock()
	return claimed, readySnap, nil
}

func buildPrepareRequest(req ClaimRequest) adapter.PrepareRequest {
	var gitInt adapter.PrepareRequest
	if req.Hooks.PrivateGit != nil {
		gitInt.GitInterceptor = req.Hooks.PrivateGit
	}
	return adapter.PrepareRequest{
		ViewID:         req.Identity.ViewID,
		RuntimeDir:     req.Storage.RuntimeDir,
		BackingDir:     req.Storage.BackingDir,
		TargetRootDirs: req.Storage.TargetRootDirs,
		Grants:         req.Grants,
		Selection:      req.Selection,
		TaskPathHints:  append([]string(nil), req.FocusPathHints...),
		ParentLayers:   req.ParentLayers,
		GitInterceptor: gitInt.GitInterceptor,
		MaxEntries:     req.Limits.MaxEntries,
		MaxBytes:       req.Limits.MaxBytes,
	}
}

func bindMountedPrivateGitLazy(req ClaimRequest, adpID adapter.AdapterID, prepared *adapter.PreparedView) error {
	var primaryTrunk string
	for _, r := range prepared.Roots {
		if r.RootID == prepared.PrimaryRootID {
			primaryTrunk = r.SourcePath
			break
		}
	}
	if primaryTrunk == "" && len(prepared.Roots) > 0 {
		primaryTrunk = prepared.Roots[0].SourcePath
	}
	return req.Hooks.PrivateGit.MountedClaimWithBinding(privategit.MountedViewBinding{
		ViewID:    req.Identity.ViewID,
		WorkerID:  req.Identity.WorkerID,
		RootID:    prepared.PrimaryRootID,
		TrunkDir:  primaryTrunk,
		MountPath: prepared.PrimaryWorkPath,
		AdapterID: string(adpID),
		Core:      prepared.Core,
		MountID:   fmt.Sprintf("%s:%s:%s", adpID, req.Identity.ViewID, prepared.PrimaryRootID),
		Active:    true,
	})
}

func verifyPreparedConfinement(
	ctx context.Context,
	adp adapter.Adapter,
	prepared *adapter.PreparedView,
	validator ConfinementValidator,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	spec, err := adp.CommandAccess(prepared.ViewID)
	if err != nil {
		return fmt.Errorf("%w: command access failed: %v", ErrConfinementFailed, err)
	}
	st, err := os.Stat(spec.WorkDir)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("%w: work_dir %q is not an accessible directory", ErrConfinementFailed, spec.WorkDir)
	}
	if filepath.Clean(spec.HiddenBackingDir) == filepath.Clean(spec.WorkDir) {
		return fmt.Errorf("%w: backing dir aliases work_dir %q", ErrConfinementFailed, spec.WorkDir)
	}
	if validator != nil {
		if err := validator(ctx, prepared); err != nil {
			return fmt.Errorf("%w: %v", ErrConfinementFailed, err)
		}
	}
	return nil
}

func buildCommittedMetadataV2(
	req ClaimRequest,
	adp adapter.Adapter,
	prepared *adapter.PreparedView,
	fallbackReason adapter.ReasonCode,
) ViewMetadataV2 {
	facts := adp.Facts()
	fbReason := facts.FallbackReason
	if fbReason == adapter.ReasonNone && facts.PreparationKind == adapter.PreparationMaterialized {
		fbReason = fallbackReason
	}
	roots := make([]RootMappingRecord, 0, len(prepared.Roots))
	for _, r := range prepared.Roots {
		roots = append(roots, RootMappingRecord{
			RootID:        r.RootID,
			Label:         r.Label,
			SourcePath:    r.SourcePath,
			Subroot:       r.Subroot,
			WorkspacePath: r.WorkspacePath,
			IsPrimary:     r.IsPrimary,
		})
	}
	var seededRepos []string
	for k := range prepared.SeededGitRepos {
		seededRepos = append(seededRepos, k)
	}
	sort.Strings(seededRepos)

	journalPath := strings.TrimSpace(req.Storage.JournalPath)
	if journalPath == "" {
		journalPath = filepath.Join(prepared.BackingDir, "journal")
	}
	return ViewMetadataV2{
		FormatVersion:  FormatVersionV2,
		Identity:       req.Identity,
		SelectedRoots:  roots,
		FocusPathHints: append([]string(nil), req.FocusPathHints...),
		Adapter: AdapterMetadataFacts{
			AdapterID:        adp.ID(),
			PreparationKind:  facts.PreparationKind,
			SourceVisibility: facts.UntouchedSourceVisibility,
			FallbackReason:   fbReason,
			CopiedEntries:    prepared.ObservedEntries,
			CopiedBytes:      prepared.ObservedBytes,
			PendingGitInit:   facts.PreparationKind == adapter.PreparationMounted,
			SeededGitRepos:   seededRepos,
		},
		JournalPath: journalPath,
		Checkpoints: CheckpointMetadataRefs{
			ParentCheckpointIDs: append([]string(nil), req.ParentCheckpointIDs...),
		},
		CommittedAt: time.Now().UTC(),
	}
}
