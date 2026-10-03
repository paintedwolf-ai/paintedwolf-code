package api

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type WorkerStatus string

const (
	WorkerStatusPending  WorkerStatus = "pending"
	WorkerStatusRunning  WorkerStatus = "running"
	WorkerStatusWaiting  WorkerStatus = "waiting"
	WorkerStatusComplete WorkerStatus = "complete"
	WorkerStatusFailed   WorkerStatus = "failed"
	WorkerStatusCanceled WorkerStatus = "canceled"
	WorkerStatusHeld     WorkerStatus = "held"
)

// IsTerminal reports whether the worker task status has reached a terminal state.
func (s WorkerStatus) IsTerminal() bool {
	switch s {
	case WorkerStatusComplete, WorkerStatusFailed, WorkerStatusCanceled:
		return true
	default:
		return false
	}
}

type SpawnReason string

const (
	SpawnReasonInitial      SpawnReason = "initial"
	SpawnReasonRetry        SpawnReason = "retry"
	SpawnReasonHumanRequest SpawnReason = "human_request"
	SpawnReasonCloseout     SpawnReason = "closeout"
)

// WorkerBlockerClass identifies a structured decision boundary.
type WorkerBlockerClass string

const (
	WorkerBlockerDecision   WorkerBlockerClass = "decision"
	WorkerBlockerCheckpoint WorkerBlockerClass = "checkpoint"
	WorkerBlockerSandbox    WorkerBlockerClass = "sandbox"
)

// WorkerResultStatusReactable reports whether a terminal result wakes the coordinator.
func WorkerResultStatusReactable(status string) bool {
	switch strings.TrimSpace(status) {
	case "", "complete", "open", "partial", "needs_decision", "failed":
		return true
	default:
		return false
	}
}

// ExecutionTarget routes worker jobs to local or remote executors.
type ExecutionTarget string

const (
	ExecutionTargetLocal  ExecutionTarget = "local"
	ExecutionTargetRunner ExecutionTarget = "runner"
)

// TaskScopeMode selects read-only or write-overlay worker behavior.
type TaskScopeMode string

const (
	TaskScopeModeRead  TaskScopeMode = "read"
	TaskScopeModeWrite TaskScopeMode = "write"
)

// WorkerTaskCharter is a bounded worker assignment.
type WorkerTaskCharter struct {
	SharedContext string   `json:"shared_context,omitempty"`
	Goal          string   `json:"goal"`
	KnownFacts    []string `json:"known_facts,omitempty"`
	Constraints   []string `json:"constraints,omitempty"`
	DoneWhen      []string `json:"done_when"`
	ContextRefs   []string `json:"context_refs,omitempty"`
}

// TaskScopeFromArgs reads scope out of task() tool args. Absent scope is read mode.
func TaskScopeFromArgs(args map[string]any) (TaskScope, error) {
	if args == nil {
		return TaskScope{Mode: TaskScopeModeRead}, nil
	}
	raw, ok := args["scope"]
	if !ok || raw == nil {
		return TaskScope{Mode: TaskScopeModeRead}, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return TaskScope{}, fmt.Errorf("scope must be an object")
	}
	var scope TaskScope
	if mode, _ := m["mode"].(string); strings.TrimSpace(mode) != "" {
		scope.Mode = TaskScopeMode(strings.TrimSpace(mode))
	}
	if base, _ := m["base_overlay_id"].(string); strings.TrimSpace(base) != "" {
		scope.BaseOverlayID = strings.TrimSpace(base)
	}
	switch paths := m["paths"].(type) {
	case []any:
		for _, item := range paths {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				scope.Paths = append(scope.Paths, strings.TrimSpace(s))
			}
		}
	case []string:
		for _, s := range paths {
			if strings.TrimSpace(s) != "" {
				scope.Paths = append(scope.Paths, strings.TrimSpace(s))
			}
		}
	}
	return scope.Normalized(), nil
}

// Normalized returns scope with default read mode and trimmed paths.
func (s TaskScope) Normalized() TaskScope {
	mode := s.Mode
	if mode == "" {
		mode = TaskScopeModeRead
	}
	paths := make([]string, 0, len(s.Paths))
	for _, p := range s.Paths {
		p = strings.TrimSpace(p)
		if p != "" {
			paths = append(paths, p)
		}
	}
	return TaskScope{
		Mode:          mode,
		Paths:         paths,
		BaseOverlayID: strings.TrimSpace(s.BaseOverlayID),
	}
}

// IsWrite reports whether task mode uses a write overlay.
func (s TaskScope) IsWrite() bool {
	return s.Normalized().Mode == TaskScopeModeWrite
}

// Summary renders task mode and focus for roster lines.
func (s TaskScope) Summary() string {
	s = s.Normalized()
	mode := string(s.Mode)
	if len(s.Paths) == 0 {
		return mode
	}
	if len(s.Paths) == 1 {
		return mode + " · focus: " + s.Paths[0]
	}
	return mode + " · focus: " + s.Paths[0] + " (+ " + strconv.Itoa(len(s.Paths)-1) + " more)"
}

// WorkerMergeStatus tracks the observable write-overlay lifecycle.
type WorkerMergeStatus string

const (
	WorkerMergeStatusPending  WorkerMergeStatus = "pending"
	WorkerMergeStatusApplying WorkerMergeStatus = "applying"
	WorkerMergeStatusMerged   WorkerMergeStatus = "merged"
	WorkerMergeStatusRebasing WorkerMergeStatus = "rebasing"
	WorkerMergeStatusOrphaned WorkerMergeStatus = "orphaned"
	WorkerMergeStatusRejected WorkerMergeStatus = "rejected"
	WorkerMergeStatusAborted  WorkerMergeStatus = "aborted"
)

// WorkerPromotePathOutcome tracks one observed path through promotion.
type WorkerPromotePathOutcome string

const (
	WorkerPromotePathOutcomeClean    WorkerPromotePathOutcome = "clean"
	WorkerPromotePathOutcomeConflict WorkerPromotePathOutcome = "conflict"
	WorkerPromotePathOutcomeApplied  WorkerPromotePathOutcome = "applied"
)

// Worker overlay promote conflict reason codes (promote_overlay JSON).
const (
	WorkerPromoteReasonThreeWayUnresolved = "three_way_unresolved"
	WorkerPromoteReasonBranchMissing      = "overlay_branch_missing"
	WorkerPromoteReasonArtifact           = "artifact_opaque"
	WorkerPromoteReasonEncodingChanged    = "encoding_changed"
)

// WorkerPromoteOrderKind classifies how a clean overlay path relates to pending siblings.
type WorkerPromoteOrderKind string

const (
	// WorkerPromoteOrderIndependent — 3-way clean; sibling order does not change outcome.
	WorkerPromoteOrderIndependent WorkerPromoteOrderKind = "independent"
	// WorkerPromoteOrderSequential shares paths with siblings.
	WorkerPromoteOrderSequential WorkerPromoteOrderKind = "sequential"
	// WorkerPromoteOrderCleanIfFirst conflicts after listed siblings land.
	WorkerPromoteOrderCleanIfFirst WorkerPromoteOrderKind = "clean_if_first"
	// WorkerPromoteOrderCleanAfter becomes clean after listed siblings land.
	WorkerPromoteOrderCleanAfter WorkerPromoteOrderKind = "clean_after"
)

// WorkerPromoteConflictTier classifies why a path conflicts (language-agnostic).
type WorkerPromoteConflictTier string

const (
	// WorkerPromoteConflictTierLineShift — high hunk_count from offset drift after siblings landed.
	WorkerPromoteConflictTierLineShift WorkerPromoteConflictTier = "line_shift"
	// WorkerPromoteConflictTierOverlappingEdit — few hunks with distinct primary vs branch bodies.
	WorkerPromoteConflictTierOverlappingEdit WorkerPromoteConflictTier = "overlapping_edit"
	// WorkerPromoteConflictTierArtifact — binary or opaque bytes; host proposes keep_ours or drop.
	WorkerPromoteConflictTierArtifact WorkerPromoteConflictTier = "artifact"
)

// WorkerPromoteResolutionAction names host-resolved merge intents for promote_overlay.
type WorkerPromoteResolutionAction string

const (
	// Plain-language merge verbs the coordinator picks per conflicting path.
	WorkerPromoteResolutionActionDrop       WorkerPromoteResolutionAction = "drop"        // don't land this path
	WorkerPromoteResolutionActionKeepTheirs WorkerPromoteResolutionAction = "keep_theirs" // take the worker's version
	WorkerPromoteResolutionActionKeepOurs   WorkerPromoteResolutionAction = "keep_ours"   // keep primary's version
	WorkerPromoteResolutionActionKeepBoth   WorkerPromoteResolutionAction = "keep_both"   // take the host's reconciled union
)

// WorkerPromoteResolution resolves one conflicting promotion path.
type WorkerPromoteResolution struct {
	Path    string                        `json:"path"`
	Action  WorkerPromoteResolutionAction `json:"action,omitempty"`
	Content string                        `json:"content,omitempty"`
	Hunks   []WorkerPromoteHunkResolution `json:"hunks,omitempty"`
}

// PromoteOverlayInput is the host promote_overlay request beyond overlay_id.
type PromoteOverlayInput struct {
	Detail      string                    `json:"detail,omitempty"`
	Resolutions []WorkerPromoteResolution `json:"resolutions,omitempty"`
	// ToolCallID links landed ledger rows to their promotion.
	ToolCallID string `json:"-"`
	// UserTurn stamps landed rows with their turn ordinal.
	UserTurn int `json:"-"`
}

// WorkerMergeResult is the promote_overlay / preview_overlay tool response.
type WorkerMergeResult struct {
	// OverlayPromotion is host-only transcript metadata.
	OverlayPromotion *OverlayPromotion             `json:"-"`
	JobID            string                        `json:"job_id"`
	Mode             string                        `json:"mode"`
	Status           WorkerMergeStatus             `json:"merge_status,omitempty"`
	AgentType        string                        `json:"agent_type,omitempty"`
	Paths            []string                      `json:"paths,omitempty"`
	ChangedPaths     []string                      `json:"changed_paths,omitempty"`
	CleanPaths       []string                      `json:"clean_paths,omitempty"`
	Applied          []string                      `json:"applied,omitempty"`
	PathStatus       []WorkerPromotePathStatus     `json:"path_status,omitempty"`
	OverlapJobIDs    []string                      `json:"overlap_job_ids,omitempty"`
	PromoteOrder     WorkerPromoteOrderKind        `json:"promote_order,omitempty"`
	PromoteAfter     []string                      `json:"promote_after,omitempty"`
	BlockedBy        []string                      `json:"blocked_by,omitempty"`
	ConflictDigest   []WorkerPromoteConflictDigest `json:"conflict_digest,omitempty"`
	Conflicts        []WorkerMergeConflict         `json:"conflicts,omitempty"`
	OverlayIntent    *WorkerOverlayIntent          `json:"overlay_intent,omitempty"`
	SpillPath        string                        `json:"spill_path,omitempty"` // host-data-relative (promote-spills/…); never absolute ~/.config/paintedwolf
	ReadyResolutions []WorkerReadyResolution       `json:"ready_resolutions,omitempty"`
	Error            string                        `json:"error,omitempty"`
	// Rebased lists children rebased after this overlay lands.
	Rebased []OverlayRebaseOutcome `json:"rebased,omitempty"`
	// SourceEvidence describes current verification requirements.
	SourceEvidence *WorkerSourceEvidence `json:"source_evidence,omitempty"`
}

// OverlayRejectOutcome reports the result of explicitly rejecting an overlay,
// including any stacked children that were orphaned by the rejection.
type OverlayRejectOutcome struct {
	OverlayID string   `json:"overlay_id"`
	Reason    string   `json:"reason,omitempty"`
	Orphaned  []string `json:"orphaned_job_ids,omitempty"`
}

// WorkspaceProvisionStrategy is selected from successful clone operations.
type WorkspaceProvisionStrategy string

const (
	WorkspaceProvisionDirectCoW  WorkspaceProvisionStrategy = "direct_cow"
	WorkspaceProvisionBridgeCoW  WorkspaceProvisionStrategy = "bridge_cow"
	WorkspaceProvisionDirectCopy WorkspaceProvisionStrategy = "direct_copy"
)

type WorkerTask struct {
	Dependencies     []WorkerDependency `json:"dependencies,omitempty"`
	AfterWorkers     []string           `json:"after_workers,omitempty"`
	ID               string             `json:"id"`
	ChildSessionID   string             `json:"child_session_id,omitempty"`
	ProjectID        string             `json:"project_id,omitempty"`
	WorkspaceRootID  string             `json:"workspace_root_id,omitempty"`
	WorkspacePath    string             `json:"workspace_path,omitempty"`
	DelegationID     string             `json:"delegation_id,omitempty"`
	LegID            string             `json:"leg_id,omitempty"`
	ParentSessionID  string             `json:"parent_session_id,omitempty"`
	SourceToolCallID string             `json:"-"`
	SourceArgsDigest string             `json:"-"`
	WorkflowRunID    string             `json:"workflow_run_id,omitempty"`
	WorkflowPhase    string             `json:"workflow_phase,omitempty"`
	WorkflowWorkID   string             `json:"workflow_work_id,omitempty"`
	AgentType        string             `json:"agent_type"`
	Status           WorkerStatus       `json:"status"`
	ExecutionTarget  ExecutionTarget    `json:"execution_target,omitempty"`
	RunnerID         string             `json:"runner_id,omitempty"`
	ClaimedBy        string             `json:"claimed_by,omitempty"`
	// Claim metadata binds updates to one durable attempt.
	ClaimToken     string      `json:"-"`
	Attempt        int         `json:"-"`
	HeartbeatAt    *time.Time  `json:"-"`
	LeaseExpiresAt *time.Time  `json:"-"`
	SpawnReason    SpawnReason `json:"spawn_reason,omitempty"`
	Prompt         string      `json:"-"`
	// Brief is the worker card title.
	Brief                 string            `json:"brief,omitempty"`
	Scope                 *TaskScope        `json:"scope,omitempty"`
	Files                 []string          `json:"files,omitempty"`
	WorkspaceRoot         string            `json:"-"`
	MergeStatus           WorkerMergeStatus `json:"merge_status,omitempty"`
	Result                *WorkerResult     `json:"result,omitempty"`
	Failure               *WorkerFailure    `json:"failure,omitempty"`
	Error                 string            `json:"error,omitempty"`
	WorkspaceBaselinePath string            `json:"-"`
	// WorkspaceOverlayPath is the sealed record of a completed write worker's
	// changes; with the baseline it rebuilds the branch tree on demand.
	WorkspaceOverlayPath string `json:"-"`
	// OverlayID identifies a write overlay and its stacked children.
	OverlayID    string `json:"overlay_id,omitempty"`
	MaxToolLoops int    `json:"max_tool_loops,omitempty"`
	// BudgetRequest is the worker's unanswered ask for a higher ceiling.
	BudgetRequest *WorkerBudgetRequest `json:"budget_request,omitempty"`
	ToolLoopsUsed int                  `json:"tool_loops_used,omitempty"`
	// ToolCallsUsed counts settled calls across all rounds.
	ToolCallsUsed int `json:"tool_calls_used,omitempty"`
	// TurnToolCalls and TurnToolsDone describe the active batch.
	TurnToolCalls int `json:"turn_tool_calls,omitempty"`
	TurnToolsDone int `json:"turn_tools_done,omitempty"`
	// ContextUsage is the worker's live context occupancy.
	ContextUsage         *WorkerContextUsage   `json:"context_usage,omitempty"`
	WorkspacePreparation *WorkspacePreparation `json:"workspace_preparation,omitempty"`
	TouchedPaths         []string              `json:"touched_paths,omitempty"`
	CreatedAt            time.Time             `json:"created_at"`
	StartedAt            *time.Time            `json:"started_at,omitempty"`
	CompletedAt          *time.Time            `json:"completed_at,omitempty"`
}

// EffectiveScope returns the job scope or default read scope when unset.
func (t WorkerTask) EffectiveScope() TaskScope {
	if t.Scope != nil {
		return t.Scope.Normalized()
	}
	return TaskScope{Mode: TaskScopeModeRead}
}

// PrimaryRootPath returns the active workspace root path denormalized on the job row.
func (t WorkerTask) PrimaryRootPath() string {
	return strings.TrimSpace(t.WorkspacePath)
}

// WorkerTaskOverlayOpen reports write workers whose branch changes are not yet on primary.
func WorkerTaskOverlayOpen(task *WorkerTask) bool {
	if task == nil || task.Status != WorkerStatusComplete {
		return false
	}
	switch task.MergeStatus {
	case WorkerMergeStatusPending, WorkerMergeStatusApplying, WorkerMergeStatusRebasing:
		return true
	case WorkerMergeStatusMerged, WorkerMergeStatusRejected, WorkerMergeStatusOrphaned, WorkerMergeStatusAborted:
		return false
	default:
		return false
	}
}

// WorkerCancelResult is the worker_cancel tool and HTTP cancel response body.
type WorkerCancelResult struct {
	JobID     string             `json:"job_id"`
	Status    WorkerStatus       `json:"status"`
	AgentType string             `json:"agent_type,omitempty"`
	Report    WorkerChangeReport `json:"change_report"`
	Result    WorkerResult       `json:"result"`
}

type OverlayRebaseOutcome struct {
	OverlayID string            `json:"overlay_id"`
	Status    WorkerMergeStatus `json:"status"`
	Conflicts []string          `json:"conflicts,omitempty"`
}

type WorkerMergeConflict struct {
	Path             string                    `json:"path"`
	Reason           string                    `json:"reason"`
	Base             string                    `json:"base,omitempty"`
	Primary          string                    `json:"primary,omitempty"`
	Branch           string                    `json:"branch,omitempty"`
	BranchDelta      string                    `json:"branch_delta,omitempty"`
	BaseDelta        string                    `json:"base_delta,omitempty"`
	Summary          []string                  `json:"summary,omitempty"`
	Hunks            []WorkerMergeHunk         `json:"hunks,omitempty"`
	OverlapWorkerIDs []string                  `json:"overlap_worker_ids,omitempty"`
	ConflictTier     WorkerPromoteConflictTier `json:"conflict_tier,omitempty"`
}

type WorkerMergeHunk struct {
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Base      string `json:"base,omitempty"`
	Primary   string `json:"primary,omitempty"`
	Branch    string `json:"branch,omitempty"`
}

type WorkerOverlayIntent struct {
	Summary    string   `json:"summary,omitempty"`
	ScopePaths []string `json:"scope_paths,omitempty"`
}

type WorkerPromoteConflictDigest struct {
	Path         string                    `json:"path"`
	Summary      []string                  `json:"summary,omitempty"`
	BranchDelta  string                    `json:"branch_delta,omitempty"`
	BaseDelta    string                    `json:"base_delta,omitempty"`
	ConflictTier WorkerPromoteConflictTier `json:"conflict_tier,omitempty"`
}

type WorkerPromoteHunkResolution struct {
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Content   string `json:"content"`
}

type WorkerReadyResolution struct {
	Path               string                        `json:"path"`
	Status             WorkerPromotePathOutcome      `json:"status"`
	PathClass          string                        `json:"path_class,omitempty"`
	ConflictTier       WorkerPromoteConflictTier     `json:"conflict_tier,omitempty"`
	Action             WorkerPromoteResolutionAction `json:"action,omitempty"`
	ProposedContent    string                        `json:"proposed_content,omitempty"`
	ProposedReconciled bool                          `json:"proposed_reconciled,omitempty"`
	SuggestedHunks     []WorkerPromoteHunkResolution `json:"suggested_hunks,omitempty"`
	NeedsReview        bool                          `json:"needs_review,omitempty"`
	Advisory           string                        `json:"advisory,omitempty"`
}

type WorkerSourceEvidence struct {
	Kind             string   `json:"kind"`
	Status           string   `json:"status"`
	SourceRevision   string   `json:"source_revision,omitempty"`
	SourceRootDigest string   `json:"source_root_digest,omitempty"`
	Reason           string   `json:"reason,omitempty"`
	EvidenceRefs     []string `json:"evidence_refs,omitempty"`
}

// WorkerOutputReady reports a successful result whose writes are available on primary.
func WorkerOutputReady(task *WorkerTask) bool {
	if task == nil || task.Status != WorkerStatusComplete || task.Result == nil {
		return false
	}
	if task.Result.Status != "complete" && task.Result.Status != "open" {
		return false
	}
	return !task.EffectiveScope().IsWrite() || task.MergeStatus == WorkerMergeStatusMerged
}

// WorkerOutputState distinguishes work in progress from outcomes requiring intervention.
func WorkerOutputState(task *WorkerTask) string {
	if WorkerOutputReady(task) {
		return "ready"
	}
	if task == nil || task.Status == WorkerStatusFailed || task.Status == WorkerStatusCanceled {
		return "blocked"
	}
	if task.Status == WorkerStatusComplete {
		if task.Result == nil || (task.Result.Status != "complete" && task.Result.Status != "open") {
			return "blocked"
		}
		if task.MergeStatus == WorkerMergeStatusRejected || task.MergeStatus == WorkerMergeStatusAborted || task.MergeStatus == WorkerMergeStatusOrphaned {
			return "blocked"
		}
	}
	return "waiting"
}

// WorkerReviewSucceeded requires a completed review and its accepted full closeout.
func WorkerReviewSucceeded(task WorkerTask) bool {
	return task.Status == WorkerStatusComplete && task.Result != nil && task.Result.CompletionReport != nil && task.Result.CompletionReport.LegStatus == "complete" && !WorkerTaskOverlayOpen(&task)
}
