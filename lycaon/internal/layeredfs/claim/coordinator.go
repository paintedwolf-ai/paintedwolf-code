package claim

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
	unsettled       map[string]*unsettledView
}

// unsettledView is a claimed or rolled-back view whose settlement failed and remains in custody.
type unsettledView struct {
	adapter          adapter.Adapter
	privateGit       *privategit.Manager
	metaPath         string
	retentionClaimID string
	holdClaimID      string
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
		unsettled:       make(map[string]*unsettledView),
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
	_, inCustody := c.unsettled[req.Identity.ViewID]
	c.mu.RUnlock()
	if inCustody {
		snap, _ := c.StateSnapshot(req.Identity.ViewID)
		return nil, snap, fmt.Errorf("%w: view %q", ErrSettlementUnresolved, req.Identity.ViewID)
	}

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
	return c.ClaimSelectedView(ctx, req, adp, probeRes)
}

// ClaimSelectedView claims a view on an adapter its caller already selected and probed.
func (c *Coordinator) ClaimSelectedView(
	ctx context.Context,
	req ClaimRequest,
	adp adapter.Adapter,
	probeRes adapter.ProbeResult,
) (*ClaimedView, WorkspaceStateSnapshot, error) {
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
	fail := func(reason adapter.ReasonCode, cause error) (*ClaimedView, WorkspaceStateSnapshot, error) {
		settleErr := c.settleView(ctx, req.Identity, &unsettledView{
			adapter: adp, privateGit: req.Hooks.PrivateGit, metaPath: metaPath, retentionClaimID: retentionClaimID,
		}, req.Hooks.Retention)
		snap := c.recordFailedClaim(req, adp.ID(), reason, cause.Error(), 0, 0)
		if settleErr == nil {
			return nil, snap, cause
		}
		snap = c.recordRecoveryRequired(req.Identity.ViewID, snap, settleErr)
		return nil, snap, errors.Join(cause, settleErr)
	}

	if req.Hooks.PrivateGit != nil && adp.Facts().PreparationKind == adapter.PreparationMounted {
		if bErr := bindMountedPrivateGitLazy(req, adp); bErr != nil {
			return fail(adapter.ReasonMountDenied, bErr)
		}
	}

	if err := checkStageBoundary(ctx, req.Hooks.Fault, StageRegisterRetention); err != nil {
		return fail(classifyClaimReason(err), err)
	}
	if req.Hooks.Retention != nil {
		cid, rErr := req.Hooks.Retention.RegisterClaim(ctx, req.Identity.WorkerID, req.Identity.ViewID, collectPreparedRootPaths(prepared.Roots))
		if rErr != nil {
			return fail(adapter.ReasonMountDenied, fmt.Errorf("%w: %v", ErrRetentionFailed, rErr))
		}
		retentionClaimID = cid
	}

	if err := checkStageBoundary(ctx, req.Hooks.Fault, StageVerifyConfinement); err != nil {
		return fail(classifyClaimReason(err), err)
	}
	if err := verifyPreparedConfinement(ctx, adp, prepared, req.Hooks.VerifyConfinement); err != nil {
		reason := adapter.ReasonBackingConfinementFailed
		if classified := classifyClaimReason(err); classified == adapter.ReasonConfinementUnavailable {
			reason = classified
		}
		return fail(reason, err)
	}

	if err := checkStageBoundary(ctx, req.Hooks.Fault, StagePersistMetadata); err != nil {
		return fail(classifyClaimReason(err), err)
	}

	meta := buildCommittedMetadataV2(req, adp, prepared, probeRes.ReasonCode)
	if err := SaveViewMetadata(metaPath, meta); err != nil {
		return fail(adapter.ReasonMountDenied, err)
	}

	readySnap := buildReadySnapshot(meta)
	claimed := &ClaimedView{
		Identity:         req.Identity,
		Adapter:          adp,
		Prepared:         prepared,
		Metadata:         meta,
		RetentionClaimID: retentionClaimID,
		State:            readySnap,
		metaPath:         metaPath,
		privateGit:       req.Hooks.PrivateGit,
	}
	c.selector.Registry.Register(prepared, adp)
	c.mu.Lock()
	delete(c.failedSnapshots, req.Identity.ViewID)
	c.views[req.Identity.ViewID] = claimed
	c.mu.Unlock()
	return claimed, readySnap, nil
}

// AdoptPreparedView records a view recovery already prepared on adp, so it settles and publishes like
// a claimed one. Recovery registered nothing with this coordinator; retention registers here.
func (c *Coordinator) AdoptPreparedView(ctx context.Context, req ClaimRequest, adp adapter.Adapter, prepared *adapter.PreparedView) (*ClaimedView, error) {
	var retentionClaimID string
	if req.Hooks.Retention != nil {
		cid, err := req.Hooks.Retention.RegisterClaim(ctx, req.Identity.WorkerID, req.Identity.ViewID, collectPreparedRootPaths(prepared.Roots))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrRetentionFailed, err)
		}
		retentionClaimID = cid
	}
	meta := buildCommittedMetadataV2(req, adp, prepared, adapter.ReasonNone)
	claimed := &ClaimedView{
		Identity: req.Identity, Adapter: adp, Prepared: prepared, Metadata: meta,
		RetentionClaimID: retentionClaimID, State: buildReadySnapshot(meta), privateGit: req.Hooks.PrivateGit,
	}
	c.selector.Registry.Register(prepared, adp)
	c.mu.Lock()
	c.views[req.Identity.ViewID] = claimed
	c.mu.Unlock()
	return claimed, nil
}

// settleView releases a view's adapter resources first and its records only after they settled.
// A failed step keeps everything recorded so far and leaves the view in custody for a retry.
func (c *Coordinator) settleView(ctx context.Context, identity ClaimIdentity, view *unsettledView, retention RetentionRegistrar) error {
	settleCtx := context.WithoutCancel(ctx)
	receipt, err := view.adapter.Release(settleCtx, identity.ViewID)
	if err == nil && !receipt.Settled {
		err = fmt.Errorf("adapter %s left view unsettled: %s", view.adapter.ID(), receipt.ReasonCode)
	}
	if err == nil && view.privateGit != nil {
		err = view.privateGit.ReleaseView(identity.ViewID)
	}
	if err == nil && retention != nil && view.retentionClaimID != "" {
		if err = retention.ReleaseClaim(view.retentionClaimID); err == nil {
			view.retentionClaimID = ""
		}
	}
	if err == nil && view.holdClaimID != "" && retention != nil {
		if err = retention.ReleaseClaim(view.holdClaimID); err == nil {
			view.holdClaimID = ""
		}
	}
	if err == nil && view.metaPath != "" {
		if rmErr := os.Remove(view.metaPath); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			err = rmErr
		}
	}
	if err == nil {
		c.mu.Lock()
		delete(c.unsettled, identity.ViewID)
		c.mu.Unlock()
		c.selector.Registry.Unregister(identity.ViewID)
		return nil
	}
	if retention != nil && view.holdClaimID == "" {
		holdID, holdErr := retention.HoldUnresolved(settleCtx, identity.WorkerID, identity.ViewID)
		if holdErr != nil {
			err = errors.Join(err, holdErr)
		} else {
			view.holdClaimID = holdID
		}
	}
	c.mu.Lock()
	c.unsettled[identity.ViewID] = view
	c.mu.Unlock()
	return fmt.Errorf("%w: view %q: %w", ErrSettlementUnresolved, identity.ViewID, err)
}

func buildPrepareRequest(req ClaimRequest) adapter.PrepareRequest {
	prepReq := adapter.PrepareRequest{
		ViewID:          req.Identity.ViewID,
		RuntimeDir:      req.Storage.RuntimeDir,
		BackingDir:      req.Storage.BackingDir,
		TargetRootDirs:  req.Storage.TargetRootDirs,
		Grants:          req.Grants,
		Selection:       req.Selection,
		TaskPathHints:   append([]string(nil), req.FocusPathHints...),
		ParentLayers:    req.ParentLayers,
		ObservationSink: req.Hooks.Observations,
		QuiesceHook:     req.Hooks.Quiesce,
		FlushClientHook: req.Hooks.FlushClient,
		MaxEntries:      req.Limits.MaxEntries,
		MaxBytes:        req.Limits.MaxBytes,
	}
	if req.Hooks.PrivateGit != nil {
		prepReq.GitInterceptor = req.Hooks.PrivateGit
	}
	return prepReq
}

func bindMountedPrivateGitLazy(req ClaimRequest, adp adapter.Adapter) error {
	supervisor, ok := adp.(adapter.MountSupervisor)
	if !ok {
		return fmt.Errorf("adapter %s holds no mount supervision to bind private Git to", adp.ID())
	}
	return supervisor.BindPrivateGit(req.Identity.ViewID, req.Identity.WorkerID, req.Hooks.PrivateGit)
}

// verifyPreparedConfinement establishes that a view is usable under enforcement. The path checks
// only catch a broken view; unequal paths prove nothing about what a worker's process can reach, so
// a mounted view is never ready without its validator.
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
	if validator == nil {
		if adp.Facts().PreparationKind == adapter.PreparationMounted {
			return fmt.Errorf("%w: %w", ErrConfinementFailed, &adapter.AdapterError{
				AdapterID: adp.ID(), Op: "verify_confinement", ReasonCode: adapter.ReasonConfinementUnavailable,
				Diagnostic: "no enforcement was supplied for a mounted view",
			})
		}
		return nil
	}
	if err := validator(ctx, prepared); err != nil {
		return fmt.Errorf("%w: %w", ErrConfinementFailed, err)
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
