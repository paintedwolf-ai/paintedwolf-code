package workflow

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/jsonvalue"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	workflowreview "github.com/lycaon/lycaon/internal/workflow/review"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReviewRepairs accounts for refused verdicts and pauses a review whose repair
// cannot converge, retaining the evidence an incomplete report needs.
type ReviewRepairs struct{ *RunManager }

// RecordReviewToolResult hands a durable transcript row to review repair accounting.
func (m *RunManager) RecordReviewToolResult(ctx context.Context, sessionID string, msg api.Message) error {
	return ReviewRepairs{m}.RecordToolResult(ctx, sessionID, msg)
}

const (
	reviewRepairsKey        = runstate.ReviewRepairsKey
	ReviewBlockedReason     = runstate.ReviewBlockedReason
	reviewRepairMaxAttempts = runstate.ReviewRepairMaxAttempts
	reviewRepairMaxRepeated = runstate.ReviewRepairMaxRepeated
)

type (
	ReviewRepairResponse = runstate.ReviewRepairResponse
	ReviewRepair         = runstate.ReviewRepair
	ReviewSnapshot       = runstate.ReviewSnapshot
)

var (
	readReviewRepairs      = runstate.ReadReviewRepairs
	CurrentReviewRepair    = runstate.CurrentReviewRepair
	resolveReviewRepair    = runstate.ResolveReviewRepair
	repairableVerdictCodes = runstate.RepairableVerdictCodes
	reviewIssueFingerprint = runstate.ReviewIssueFingerprint
)

// RecordToolResult accounts for schema and semantic refusals alike, after the
// screened result is durable. Transcript replay uses the same response id.
func (m ReviewRepairs) RecordToolResult(ctx context.Context, sessionID string, msg api.Message) error {
	result := msg.ToolResult
	if result == nil || result.Tool != "submit_verdict" || result.Outcome != api.ToolResultOutcomeRejected {
		return nil
	}
	if !repairableVerdictCodes(result.Codes) {
		return nil
	}
	run, err := m.Store.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || run == nil {
		return err
	}
	unlock := m.Vars.Lock(run.ID)
	defer unlock()
	run, err = m.Store.Runs.Get(ctx, run.ID)
	if err != nil {
		return err
	}
	if run.Status != api.WorkflowRunStatusRunning || (msg.WorkflowRunID != "" && msg.WorkflowRunID != run.ID) {
		return nil
	}
	for _, feedback := range result.Feedback {
		if offeredPhase, ok := feedback.Details["workflow_phase"].(string); ok && offeredPhase != run.CurrentPhase {
			return nil
		}
	}
	if m.Sessions == nil {
		return fmt.Errorf("review repair requires transcript storage")
	}
	messages, err := m.Sessions.GetMessages(ctx, sessionID)
	if err != nil {
		return err
	}
	settled, err := finalReviewResponseResult(messages, msg)
	if err != nil || !settled {
		return err
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return err
	}
	phase, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok || phase.ReviewLoop == nil {
		return nil
	}
	vars, err := m.Store.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return err
	}
	rows, err := readReviewRepairs(vars)
	if err != nil {
		return err
	}
	response := result.AssistantMessageID
	if response == "" {
		response = result.ToolCallID
	}
	if response == "" || msg.ID == "" {
		return fmt.Errorf("review repair requires durable result and response identities")
	}
	for _, prior := range rows {
		if slices.ContainsFunc(prior.Responses, func(r ReviewRepairResponse) bool { return r.ID == response }) && (prior.Phase != run.CurrentPhase || prior.State != "repairing") {
			return nil
		}
	}
	now := time.Now().UTC()
	if len(rows) == 0 || rows[len(rows)-1].Phase != run.CurrentPhase || rows[len(rows)-1].State != "repairing" {
		rows = append(rows, ReviewRepair{ID: uuid.NewString(), Phase: run.CurrentPhase, State: "repairing", CreatedAt: now})
	}
	episode := &rows[len(rows)-1]
	if !episode.ObserveResponse(response, msg.ID, reviewIssueFingerprint(result.Feedback)) {
		return nil
	}
	episode.CandidateMessageID = msg.ID
	episode.Diagnostics = result.Feedback
	episode.UpdatedAt = now
	blocked := episode.Repeated >= reviewRepairMaxRepeated || len(episode.Responses) >= reviewRepairMaxAttempts
	var boundary *api.Message
	if blocked {
		episode.State = "blocked"
		snapshot := m.captureSnapshot(ctx, run, manifest, vars, result.ToolArgs)
		episode.Snapshot = &snapshot
		if phase.ReviewLoop.CarriesCoverage() && len(snapshot.Unavailable) == 0 {
			facts := workflowreview.BuildCoverageFacts(manifest, vars, snapshot.Workers, snapshot.Scans)
			episode.CoverageFacts = &facts
		}

		run.Status = api.WorkflowRunStatusPaused
		run.PauseReason = ReviewBlockedReason
		run.PausedAt = &now
		b := runstate.NewCommandBoundary(run, run.Revision, "paused", run.CurrentPhase, ReviewBlockedReason)
		boundary = &b
	}
	run.UpdatedAt = now
	vars = maps.Clone(vars)
	vars[reviewRepairsKey] = rows
	if err := m.Journal.Commit(ctx, run, "review_repair", struct{ MessageID string }{msg.ID}, vars, boundary, "", runstate.WorkerMutation{HoldPending: blocked}, nil); err != nil {
		return err
	}
	m.Publication.PublishSession(ctx, run)
	return nil
}

// Recover closes the crash window between transcript commit and repair
// accounting. Accepted submissions delimit the current repair sequence.
func (m ReviewRepairs) Recover(ctx context.Context) error {
	if m.Sessions == nil {
		return nil
	}
	runs, err := m.Store.Runs.ListRunning(ctx)
	if err != nil {
		return err
	}
	for _, run := range runs {
		messages, err := m.Sessions.GetMessages(ctx, run.SessionID)
		if err != nil {
			return err
		}
		start := 0
		for i, msg := range messages {
			if msg.WorkflowRunID == run.ID && msg.ToolResult != nil && msg.ToolResult.Tool == "submit_verdict" && msg.ToolResult.Outcome == api.ToolResultOutcomeCompleted {
				start = i + 1
			}
		}
		for _, msg := range messages[start:] {
			if msg.WorkflowRunID != run.ID {
				continue
			}
			if err := m.RecordToolResult(ctx, run.SessionID, msg); err != nil {
				return err
			}
		}
	}
	return nil
}

// A response can submit multiple calls. Repair admission waits for every
// verdict result, so an earlier rejection cannot interrupt a later correction.
func finalReviewResponseResult(messages []api.Message, candidate api.Message) (bool, error) {
	response := candidate.ToolResult.AssistantMessageID
	expected := map[string]bool{}
	found := false
	for _, msg := range messages {
		if msg.ID != response || msg.Role != api.MessageRoleAssistant {
			continue
		}
		found = true
		for _, call := range msg.ToolCalls {
			if call.Name == "submit_verdict" {
				expected[call.ID] = true
			}
		}
	}
	if !found || len(expected) == 0 {
		return false, fmt.Errorf("review repair requires the durable assistant call batch")
	}
	last := ""
	for _, msg := range messages {
		result := msg.ToolResult
		if result == nil || result.AssistantMessageID != response || result.Tool != "submit_verdict" {
			continue
		}
		if _, exists := expected[result.ToolCallID]; exists {
			expected[result.ToolCallID] = false
			last = msg.ID
		}
	}
	for _, pending := range expected {
		if pending {
			return false, nil
		}
	}
	return last == candidate.ID, nil
}

func (m ReviewRepairs) captureSnapshot(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, vars map[string]any, candidate map[string]any) ReviewSnapshot {
	snapshot := ReviewSnapshot{Vars: map[string]any{}, Candidate: jsonvalue.CloneMap(candidate)}
	// Only report inputs are copied, excluding repair episodes themselves.
	for _, key := range []string{"fanout_plans"} {
		if value, ok := vars[key]; ok {
			snapshot.Vars[key] = value
		}
	}
	if m.Coverage != nil && m.Coverage.WorkerTasks != nil {
		workers, err := m.Coverage.WorkerTasks(ctx, run.ID)
		if err != nil {
			snapshot.Unavailable = append(snapshot.Unavailable, "worker ledger")
		} else {
			snapshot.Workers = workers
		}
	} else {
		snapshot.Unavailable = append(snapshot.Unavailable, "worker ledger")
	}
	if m.Coverage != nil && m.Coverage.Inventory != nil {
		inventory, err := workflowreview.LoadRunInventory(ctx, m.Coverage.Inventory, run.ID)
		if err != nil {
			snapshot.Unavailable = append(snapshot.Unavailable, "scan ledger")
		} else {
			snapshot.Scans = inventory.Scans
		}
	} else {
		snapshot.Unavailable = append(snapshot.Unavailable, "scan ledger")
	}
	verdicts, err := workflowpresentation.ReviewVerdicts(ctx, m.Verdicts, run, manifest)
	snapshot.Verdicts = verdicts
	if err != nil {
		snapshot.Unavailable = append(snapshot.Unavailable, "review evidence ledger")
	}
	return snapshot
}

const reviewContractInvalidCode = "WORKFLOW_REVIEW_CONTRACT_INVALID"

// BlockContract pauses a review whose offered contract cannot accept the
// host's own facts, before any model attempt is spent.
func (m ReviewRepairs) BlockContract(ctx context.Context, runID string, cause error) error {
	unlock := m.Vars.Lock(runID)
	defer unlock()
	run, err := m.Store.Runs.Get(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status != api.WorkflowRunStatusRunning {
		return nil
	}
	vars, err := m.Store.Runs.GetScaffoldVars(ctx, runID)
	if err != nil {
		return err
	}
	repairs, err := readReviewRepairs(vars)
	if err != nil {
		return err
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return err
	}
	snapshot := m.captureSnapshot(ctx, run, manifest, vars, nil)
	now := time.Now().UTC()
	repair := ReviewRepair{Snapshot: &snapshot, ID: uuid.NewString(), Phase: run.CurrentPhase, State: "blocked", CreatedAt: now, UpdatedAt: now, Diagnostics: []api.ToolFeedback{{Code: reviewContractInvalidCode, Details: map[string]any{"reason": cause.Error()}}}}
	if len(snapshot.Unavailable) == 0 {
		facts := workflowreview.BuildCoverageFacts(manifest, vars, snapshot.Workers, snapshot.Scans)
		repair.CoverageFacts = &facts
	}
	repairs = append(repairs, repair)

	vars = maps.Clone(vars)
	vars[reviewRepairsKey] = repairs
	run.Status = api.WorkflowRunStatusPaused
	run.PauseReason = ReviewBlockedReason
	run.PausedAt = &now
	run.UpdatedAt = now
	boundary := runstate.NewCommandBoundary(run, run.Revision, "paused", run.CurrentPhase, ReviewBlockedReason)
	if err := m.Journal.Commit(ctx, run, "review_contract_blocked", struct{ Code string }{reviewContractInvalidCode}, vars, &boundary, "", runstate.WorkerMutation{HoldPending: true}, nil); err != nil {
		return err
	}
	m.Publication.PublishSession(ctx, run)
	return nil
}

func (m ReviewRepairs) blockContract(ctx context.Context, runID string, cause error) error {
	return m.BlockContract(ctx, runID, cause)
}
