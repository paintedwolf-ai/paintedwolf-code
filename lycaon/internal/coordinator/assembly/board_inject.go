package assembly

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/pkg/api"
)

type BoardSnapshotBuilder interface {
	BuildBoardSnapshot(ctx context.Context, projectID, workspacePath, sessionID string, level api.BoardDetailLevel, roots []projectroot.RootRef) (*api.BoardSnapshot, error)
}

type BoardPackFormatter interface {
	FormatBoardInject(snap api.BoardSnapshot, omitDelegation bool, now time.Time) (body string, ok bool)
}

type boardInjectState struct {
	lastOrientationFP string
	lastPulseFP       string
	lastWorkflowRunID string
	lastWorkflowPhase string
	lastInjectKey     string
	injectedOnce      bool
}

type BoardEngine struct {
	workerRoots func(context.Context, *api.Session) ([]projectroot.RootRef, error)
	builder     BoardSnapshotBuilder
	formatter   BoardPackFormatter
	injects     atomic.Pointer[prompts.InjectRenderer]
	// state bounds fingerprints; eviction only forces reinjection.
	state              *scopedstore.LRU[boardInjectState]
	omitDelegation     func() bool
	includeScanLegend  func() bool
	promotePathOverlay func(sessionID string) []api.WorkerPromoteJobPathStatus
	overlayMergePlan   func(sessionID string, tasks []api.WorkerTask) *api.OverlayMergePlan
	activeReservations func(sessionID string) []api.BoardReservationEntry
}

func NewBoardEngine(builder BoardSnapshotBuilder, formatter BoardPackFormatter, omitDelegation func() bool) *BoardEngine {
	return &BoardEngine{
		builder:        builder,
		formatter:      formatter,
		omitDelegation: omitDelegation,
		state:          scopedstore.New[boardInjectState](scopedstore.DefaultEntries),
	}
}

// WithRepositoryFacts shares repository reads across board-driven phase changes.
func (b *BoardEngine) WithRepositoryFacts(ctx context.Context) context.Context {
	if scoped, ok := b.builder.(interface {
		WithRepositoryFacts(context.Context) context.Context
	}); ok {
		return scoped.WithRepositoryFacts(ctx)
	}
	return ctx
}

// SetWorkerRoots binds board orientation to the same roots worker tools use.
func (b *BoardEngine) SetWorkerRoots(resolve func(context.Context, *api.Session) ([]projectroot.RootRef, error)) {
	b.workerRoots = resolve
}

func (b *BoardEngine) SetBuilder(builder BoardSnapshotBuilder, formatter BoardPackFormatter) {
	if b == nil {
		return
	}
	b.builder = builder
	b.formatter = formatter
}

func (b *BoardEngine) SetPromotePathOverlay(fn func(sessionID string) []api.WorkerPromoteJobPathStatus) {
	if b == nil {
		return
	}
	b.promotePathOverlay = fn
}

func (b *BoardEngine) SetOverlayMergePlan(fn func(sessionID string, tasks []api.WorkerTask) *api.OverlayMergePlan) {
	if b == nil {
		return
	}
	b.overlayMergePlan = fn
}

func (b *BoardEngine) SetActiveReservations(fn func(sessionID string) []api.BoardReservationEntry) {
	if b == nil {
		return
	}
	b.activeReservations = fn
}

func (b *BoardEngine) SetInjectRenderer(renderer *prompts.InjectRenderer) {
	if b == nil {
		return
	}
	b.injects.Store(renderer)
}

// InvalidateOrientation forces the next prompt to inject a full board.
func (b *BoardEngine) InvalidateOrientation(sessionID string) {
	if b == nil {
		return
	}
	st, _ := b.state.Load(sessionID)
	st.lastOrientationFP = ""
	st.injectedOnce = false
	b.state.Store(sessionID, st)
}

func boardWorkerSessionID(sess *api.Session) string {
	if sess == nil {
		return ""
	}
	if sess.IsWorkerChild() {
		return strings.TrimSpace(sess.ParentSessionID)
	}
	return strings.TrimSpace(sess.ID)
}

// Each run receives orientation even when its board and phase match the previous run.
func (b *BoardEngine) PrependBoardIfChanged(
	ctx context.Context,
	sess *api.Session,
	run api.CoordinatorRunContext,
) (string, bool) {
	if b == nil || sess == nil || b.builder == nil {
		return "", false
	}
	if b.injects.Load() == nil && b.formatter == nil {
		return "", false
	}
	if !surface.IsCoordinatorSession(sess) {
		return "", false
	}
	projectID := strings.TrimSpace(sess.ProjectID)
	workspacePath := strings.TrimSpace(sess.WorkspacePath)
	if projectID == "" || workspacePath == "" {
		return "", false
	}
	snap, err := b.builder.BuildBoardSnapshot(ctx, projectID, workspacePath, boardWorkerSessionID(sess), api.BoardDetailLevelCompact, nil)
	if err != nil || snap == nil {
		return "", false
	}
	if b.promotePathOverlay != nil || b.overlayMergePlan != nil {
		packboard.EnrichSnapshotOverlay(boardWorkerSessionID(sess), snap, b.promotePathOverlay, b.overlayMergePlan)
	}
	if b.activeReservations != nil {
		packboard.EnrichSnapshotReservationsForSession(boardWorkerSessionID(sess), snap, b.activeReservations)
	}
	now := time.Now().UTC()
	orientationFP := packboard.OrientationFingerprint(*snap)
	pulseFP := packboard.PulseFingerprint(*snap, now)
	phase := strings.TrimSpace(run.CurrentPhase)
	st, _ := b.state.Load(sess.ID)
	decision := decideBoardInject(st, orientationFP, pulseFP, run.RunID, phase)
	if !decision.Inject {
		return "", false
	}
	omitDelegation := false
	if b.omitDelegation != nil {
		omitDelegation = b.omitDelegation()
	}
	includeLegend := true
	if b.includeScanLegend != nil {
		includeLegend = b.includeScanLegend()
	}
	block, ok := b.renderInjectBlock(ctx, sess.ID, *snap, decision.Scope, omitDelegation, includeLegend, now)
	if !ok || strings.TrimSpace(block) == "" {
		return "", false
	}
	st.injectedOnce = true
	st.lastOrientationFP = decision.OrientationFP
	st.lastPulseFP = decision.PulseFP
	st.lastWorkflowRunID = run.RunID
	st.lastWorkflowPhase = phase
	st.lastInjectKey = decision.cacheKey(run.RunID, phase)
	b.state.Store(sess.ID, st)
	return block, true
}

// WorkerBoard refreshes worker workspace orientation for each model request.
func (b *BoardEngine) WorkerBoard(
	ctx context.Context,
	sess *api.Session,
) (string, bool) {
	if b == nil || sess == nil || b.builder == nil {
		return "", false
	}
	if b.injects.Load() == nil && b.formatter == nil {
		return "", false
	}
	if !sess.IsWorkerChild() {
		return "", false
	}
	projectID := strings.TrimSpace(sess.ProjectID)
	workspacePath := strings.TrimSpace(sess.WorkspacePath)
	if projectID == "" || workspacePath == "" {
		return "", false
	}
	var roots []projectroot.RootRef
	if b.workerRoots != nil {
		var err error
		roots, err = b.workerRoots(ctx, sess)
		if err != nil || len(roots) == 0 {
			return "", false
		}
		active, err := projectroot.ActiveRoot(roots, sess.WorkspaceRootID)
		if err != nil {
			return "", false
		}
		workspacePath = active.Path
	}
	snap, err := b.builder.BuildBoardSnapshot(ctx, projectID, workspacePath, boardWorkerSessionID(sess), api.BoardDetailLevelCompact, roots)
	if err != nil || snap == nil {
		return "", false
	}
	if b.promotePathOverlay != nil || b.overlayMergePlan != nil {
		packboard.EnrichSnapshotOverlay(boardWorkerSessionID(sess), snap, b.promotePathOverlay, b.overlayMergePlan)
	}
	if b.activeReservations != nil {
		packboard.EnrichSnapshotReservationsForSession(boardWorkerSessionID(sess), snap, b.activeReservations)
	}
	now := time.Now().UTC()
	orientationFP := packboard.OrientationFingerprint(*snap)
	pulseFP := packboard.PulseFingerprint(*snap, now)
	st, _ := b.state.Load(sess.ID)
	block, ok := b.renderWorkerBoardBlock(ctx, sess.ID, *snap, packboard.InjectScopeFull, true, false, now, workspacePath)
	if !ok || strings.TrimSpace(block) == "" {
		return "", false
	}
	st.injectedOnce = true
	st.lastOrientationFP = orientationFP
	st.lastPulseFP = pulseFP
	st.lastInjectKey = orientationPulseCacheKey(orientationFP, pulseFP, "", "")
	b.state.Store(sess.ID, st)
	return block, true
}

func (b *BoardEngine) renderInjectBlock(
	ctx context.Context,
	sessionID string,
	snap api.BoardSnapshot,
	scope packboard.InjectScope,
	omitDelegation bool,
	includeScanLegend bool,
	now time.Time,
) (string, bool) {
	if inj := b.injects.Load(); inj != nil {
		rendered, err := inject.RenderBoardOrientationInject(ctx, inj, sessionID, snap, scope, omitDelegation, includeScanLegend, now)
		if err != nil {
			return "", false
		}
		return rendered, rendered != ""
	}
	if b.formatter != nil {
		body, ok := b.formatter.FormatBoardInject(snap, omitDelegation, now)
		if !ok || strings.TrimSpace(body) == "" {
			return "", false
		}
		return "## Pack board (host)\n\n" + body, true
	}
	return "", false
}

func (b *BoardEngine) renderWorkerBoardBlock(
	ctx context.Context,
	sessionID string,
	snap api.BoardSnapshot,
	scope packboard.InjectScope,
	omitDelegation bool,
	includeScanLegend bool,
	now time.Time,
	workspacePath string,
) (string, bool) {
	if inj := b.injects.Load(); inj != nil {
		rendered, err := inject.RenderWorkerBoardInject(ctx, inj, sessionID, snap, scope, omitDelegation, includeScanLegend, now, workspacePath)
		if err != nil {
			return "", false
		}
		return rendered, rendered != ""
	}
	return b.renderInjectBlock(ctx, sessionID, snap, scope, omitDelegation, includeScanLegend, now)
}

func (b *BoardEngine) BoardInjectHash(sessionID string) string {
	if b == nil {
		return ""
	}
	st, _ := b.state.Load(sessionID)
	return st.lastInjectKey
}

func (b *BoardEngine) BoardWillForceInject(ctx context.Context, sess *api.Session, run api.CoordinatorRunContext) bool {
	if b == nil || sess == nil || b.builder == nil {
		return false
	}
	projectID := strings.TrimSpace(sess.ProjectID)
	workspacePath := strings.TrimSpace(sess.WorkspacePath)
	if projectID == "" || workspacePath == "" {
		return false
	}
	snap, err := b.builder.BuildBoardSnapshot(ctx, projectID, workspacePath, boardWorkerSessionID(sess), api.BoardDetailLevelCompact, nil)
	if err != nil || snap == nil {
		return false
	}
	if b.promotePathOverlay != nil || b.overlayMergePlan != nil {
		packboard.EnrichSnapshotOverlay(boardWorkerSessionID(sess), snap, b.promotePathOverlay, b.overlayMergePlan)
	}
	if b.activeReservations != nil {
		packboard.EnrichSnapshotReservationsForSession(boardWorkerSessionID(sess), snap, b.activeReservations)
	}
	now := time.Now().UTC()
	st, _ := b.state.Load(sess.ID)
	decision := decideBoardInject(
		st,
		packboard.OrientationFingerprint(*snap),
		packboard.PulseFingerprint(*snap, now),
		run.RunID,
		strings.TrimSpace(run.CurrentPhase),
	)
	return decision.Inject
}

func (b *BoardEngine) SetOmitDelegation(fn func() bool) {
	if b == nil {
		return
	}
	b.omitDelegation = fn
}

func (b *BoardEngine) SetIncludeScanLegend(fn func() bool) {
	if b == nil {
		return
	}
	b.includeScanLegend = fn
}
