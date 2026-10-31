package worker

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/scan/obligation"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/pkg/api"
)

// Overlay promote reject codes map to structured guidance.
const (
	OverlayPromoteNotFoundCode            = "OVERLAY_PROMOTE_NOT_FOUND"
	OverlayPromoteSessionMismatchCode     = "OVERLAY_PROMOTE_SESSION_MISMATCH"
	OverlayPromoteNotPendingCode          = "OVERLAY_PROMOTE_NOT_PENDING"
	OverlayPromoteNoPathsCode             = "OVERLAY_PROMOTE_NO_PATHS"
	OverlayPromoteConflictCode            = "OVERLAY_PROMOTE_CONFLICT"
	OverlayPromoteResolutionsRequiredCode = "OVERLAY_PROMOTE_RESOLUTIONS_REQUIRED"
	OverlayPromoteSyntaxUnhealthyCode     = "OVERLAY_PROMOTE_SYNTAX_UNHEALTHY"
)

// MergeQueue loads worker jobs and materializes their branches.
type MergeQueue interface {
	Get(jobID string) (*api.WorkerTask, bool)
	WorkerBranches
}

// MergeStore persists leased promotion state.
type MergeStore interface {
	BeginMergeApply(ctx context.Context, jobID string) (claimToken string, ok bool, err error)
	RenewMergeApply(ctx context.Context, jobID, claimToken string) (bool, error)
	ReleaseMergeApply(ctx context.Context, jobID, claimToken string) error
	ReclaimExpiredMergeApply(ctx context.Context, jobID string) (claimToken string, ok bool, err error)
	ListMergeApplying(ctx context.Context) ([]string, error)
	SetMergeStatus(ctx context.Context, jobID string, status api.WorkerMergeStatus) error
	SetMergeStatuses(ctx context.Context, updates []MergeStatusUpdate) error
	CommitPromotion(ctx context.Context, jobID, claimToken string, commit PromotionCommit) error
	ClearWorkerWorkspace(ctx context.Context, jobID string) error
}

type MergeStatusUpdate struct {
	JobID  string
	Status api.WorkerMergeStatus
}

type PromotionCommit struct {
	Plan     obligation.Plan
	Recorder sourceledger.BatchRecorder
	// Documents writes staged editor imports before Records, whose text states reference them.
	Documents     PromotedDocumentWriter
	Records       []sourceledger.RecordInput
	Changes       []sourcefeed.Change
	ChildStatuses []MergeStatusUpdate
}

// PromotedDocumentWriter joins the promotion transaction with staged editor document imports.
type PromotedDocumentWriter interface {
	CommitTx(ctx context.Context, tx *sql.Tx) error
}

// MergeSessionLister supplies pending and live overlay sets.
type MergeSessionLister interface {
	ListPendingOverlayPromote(ctx context.Context, sessionID string) ([]api.WorkerTask, error)
	ListLiveOverlaysForSession(ctx context.Context, sessionID string) ([]api.WorkerTask, error)
}

// WorkerCoordinationCleanup releases landed worker coordination state.
type WorkerCoordinationCleanup interface {
	ReleaseReservations(ctx context.Context, parentSessionID, jobID string) error
}

// DelegationCloseoutRetry re-evaluates closeout after promotion.
type DelegationCloseoutRetry interface {
	RetryCloseout(ctx context.Context, delegationID string) error
}

// OverlayScanHook runs promotion scan obligations.
type OverlayScanHook interface {
	PrepareOverlayPromotion(ctx context.Context, task api.WorkerTask, appliedPaths, deletedPaths []string) (obligation.Plan, error)
	PublishObligation(ctx context.Context, plan obligation.Plan) error
}

// OverlayDocumentSynchronizer holds open editor documents across a promotion so
// their imports commit atomically with it.
type OverlayDocumentSynchronizer interface {
	HoldPromotedDocuments(ctx context.Context, p *project.Project, refs []editordoc.PathRef) (editordoc.PromotedDocumentHold, error)
}

// MergeService applies coordinator merge tools to worker branches.
type MergeService struct {
	Queue         MergeQueue
	Store         MergeStore
	Reports       ChangeReportDeps
	Evidence      SourceEvidenceContext
	Workspace     WorkerWorkspaceManager
	Reject        *guidance.StaticRejectFormatter
	Sessions      MergeSessionLister
	Reconcile     MergeReconcileRegistrar
	Captures      PromoteCapture
	Coord         WorkerCoordinationCleanup
	Closeout      DelegationCloseoutRetry
	Projects      ProjectStore
	Scans         OverlayScanHook
	SourceLedger  sourceledger.PromoteRecorder
	SourceHistory sourceledger.JobHistory
	Documents     OverlayDocumentSynchronizer
	DataDir       string
}

// PreviewForSession assesses one overlay without changing state.
func (s *MergeService) PreviewForSession(ctx context.Context, sessionID, jobID, detail string, paths []string) (api.WorkerMergeResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	jobID = strings.TrimSpace(jobID)
	if s == nil || s.Queue == nil {
		return api.WorkerMergeResult{}, fmt.Errorf("overlay promote service not configured")
	}
	task, ok := s.Queue.Get(jobID)
	if !ok || task == nil {
		return api.WorkerMergeResult{}, s.reject(OverlayPromoteNotFoundCode, map[string]any{"job_id": jobID})
	}
	if strings.TrimSpace(task.ParentSessionID) != sessionID {
		return api.WorkerMergeResult{}, s.reject(OverlayPromoteSessionMismatchCode, map[string]any{"job_id": jobID})
	}
	if strings.TrimSpace(task.WorkspaceRoot) == "" {
		return api.WorkerMergeResult{}, s.reject(OverlayPromoteNotPendingCode, map[string]any{"job_id": jobID, "reason": "no isolated branch"})
	}
	if task.MergeStatus != api.WorkerMergeStatusPending {
		return api.WorkerMergeResult{}, s.reject(OverlayPromoteNotPendingCode, map[string]any{
			"job_id":       jobID,
			"merge_status": string(task.MergeStatus),
		})
	}
	eng := s.engine()
	lease, err := eng.holdBranch(ctx, jobID)
	if err != nil {
		return api.WorkerMergeResult{}, err
	}
	defer lease.Release()
	out := api.WorkerMergeResult{
		JobID:     jobID,
		Mode:      "preview",
		AgentType: task.AgentType,
	}
	opts := promoteOutputOpts{Detail: detail}
	mergePaths, err := eng.resolvePreviewMergePaths(ctx, jobID, task, paths)
	if err != nil {
		return api.WorkerMergeResult{}, err
	}
	out.Paths = mergePaths
	assessment, err := eng.assessMerge(ctx, sessionID, task, mergePaths)
	if err != nil {
		return api.WorkerMergeResult{}, err
	}
	fillMergeResult(&out, assessment, opts)
	s.attachPromoteSpill(s.hostDataDirFor(task), jobID, &out, assessment)
	eng.registerReconcilePaths(sessionID, assessment)
	enrichMergeChangedPaths(&out, *task, s.reports(), ctx)
	EnrichOverlayIntent(&out, task)
	out.Status = api.WorkerMergeStatusPending
	eng.attachSourceEvidence(ctx, task, &out)
	eng.recordPromoteOutput(sessionID, jobID, &out)
	return out, nil
}

func (s mergeEngine) resolvePreviewMergePaths(ctx context.Context, jobID string, task *api.WorkerTask, paths []string) ([]string, error) {
	roots := s.taskRootRefs(ctx, task)
	changed, err := session.InspectOverlayChanges(ctx, task, roots)
	if err != nil {
		return nil, err
	}
	mergePaths := normalizeMergePaths(ctx, paths, *task, roots)
	if len(paths) > 0 {
		validated, err := validatePromotePaths(task, roots, s.jobPromotePaths(ctx, task), mergePaths)
		if err != nil {
			return nil, err
		}
		mergePaths = validated
	}
	if len(mergePaths) == 0 {
		return nil, s.reject(OverlayPromoteNoPathsCode, map[string]any{"job_id": jobID, "remaining_overlay_paths": changed})
	}
	return mergePaths, nil
}

type mergeEngine struct {
	*MergeService
}

func (s *MergeService) engine() mergeEngine {
	return mergeEngine{MergeService: s}
}

func (s mergeEngine) overlayMessages(ctx context.Context, task *api.WorkerTask) []api.Message {
	if s.MergeService == nil || task == nil || s.Reports.Messages == nil {
		return nil
	}
	childID := strings.TrimSpace(task.ChildSessionID)
	if childID == "" {
		return nil
	}
	msgs, err := s.Reports.Messages(ctx, childID)
	if err != nil {
		return nil
	}
	return msgs
}

func (s mergeEngine) attachSourceEvidence(ctx context.Context, task *api.WorkerTask, out *api.WorkerMergeResult) {
	if s.MergeService == nil || out == nil || task == nil {
		return
	}
	proof := session.OverlaySourceProof(ctx, task, s.overlayMessages(ctx, task), s.Evidence.command(ctx, task), s.Evidence.revision(ctx, task))
	if s.Reports.SourceRuns != nil {
		runs, err := s.Reports.SourceRuns(ctx, task.ChildSessionID)
		if err != nil {
			out.SourceEvidence = &api.WorkerSourceEvidence{Kind: "source_validation", Status: workercompletion.EvidenceStatusUnmet, Reason: "Validation evidence could not be loaded."}
			return
		}
		proof = proof.WithSourceRuns(runs)
	}
	status, reason, refs := workercompletion.SourceEvidenceView(proof)
	ev := &api.WorkerSourceEvidence{
		Kind:             "source_validation",
		Status:           status,
		SourceRevision:   proof.SourceRevision,
		SourceRootDigest: proof.SourceRootDigest,
	}
	if status != workercompletion.EvidenceStatusSatisfied {
		ev.Status = workercompletion.EvidenceStatusUnmet
		ev.Reason = reason
	} else {
		ev.EvidenceRefs = refs
	}
	out.SourceEvidence = ev
}

func (s mergeEngine) assessMerge(ctx context.Context, sessionID string, task *api.WorkerTask, mergePaths []string) (PromoteAssessment, error) {
	if task == nil {
		return PromoteAssessment{}, nil
	}
	return AssessPromotePaths3Way(ctx, s.MergeService, sessionID, task, mergePaths)
}

func (s *MergeService) hostDataDirFor(task *api.WorkerTask) string {
	if task == nil {
		return ""
	}
	dataDir := ""
	if s != nil {
		dataDir = s.DataDir
	}
	if id := strings.TrimSpace(task.ProjectID); id != "" {
		dir, err := project.EnsureHostDataDir(dataDir, id)
		if err == nil {
			return dir
		}
	}
	if dir := project.PathKeyedHostDataDir(dataDir, task.WorkspacePath); dir != "" {
		_ = os.MkdirAll(dir, 0o700)
		return dir
	}
	return strings.TrimSpace(task.WorkspacePath)
}

func (s *MergeService) reports() ChangeReportDeps {
	if s == nil {
		return ChangeReportDeps{}
	}
	return s.Reports
}

func (s *MergeService) reject(code string, data map[string]any) error {
	var formatter *guidance.StaticRejectFormatter
	if s != nil {
		formatter = s.Reject
	}
	return toolrejection.FormatDecisionReject(code, data, formatter)
}

func normalizeMergePaths(ctx context.Context, paths []string, task api.WorkerTask, roots []projectroot.RootRef) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, p := range paths {
		add(p)
	}
	if len(out) > 0 {
		return out
	}
	for _, p := range session.OverlayWorkspaceChangedPaths(ctx, &task, roots) {
		add(p)
	}
	return out
}

func (s *MergeService) taskRootRefs(ctx context.Context, task *api.WorkerTask) []projectroot.RootRef {
	var projects ProjectStore
	if s != nil {
		projects = s.Projects
	}
	return TaskRootRefs(ctx, task, projects)
}

type MergeReconcileRegistrar interface {
	SetMergeReconcilePaths(sessionID string, paths []string)
	ClearMergeReconcilePaths(sessionID string)
	RecordPromotePathStatus(sessionID, jobID string, statuses []api.WorkerPromotePathStatus)
	RecordOverlayPreviewSummary(sessionID, jobID string, out *api.WorkerMergeResult)
	ClearPromotePathStatus(sessionID, jobID string)
}
type PromoteCapture interface {
	RecordPromotedPrimaryPaths(context.Context, string, []string)
}
