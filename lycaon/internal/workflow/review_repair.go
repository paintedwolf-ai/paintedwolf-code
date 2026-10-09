package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/internal/reviewcoverage"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReviewRepairs accounts for refused verdicts and pauses a review whose repair
// cannot converge, retaining the evidence an incomplete report needs.
type ReviewRepairs struct{ *RunManager }

// RecordReviewToolResult hands a durable transcript row to review repair accounting.
func (m *RunManager) RecordReviewToolResult(ctx context.Context, sessionID string, msg api.Message) error {
	return ReviewRepairs{m}.RecordToolResult(ctx, sessionID, msg)
}

const reviewRepairsKey = "review_repairs"

const ReviewBlockedReason = "review_blocked"

const reviewRepairMaxAttempts = 8

const reviewRepairMaxRepeated = 3

// ReviewRepairResponse deduplicates results from one model response.
type ReviewRepairResponse struct {
	ID          string   `json:"id"`
	Results     []string `json:"results"`
	Fingerprint string   `json:"fingerprint"`
}

// ReviewRepair is committed with workflow state; candidate snapshots are screened.
type ReviewRepair struct {
	Snapshot           *ReviewSnapshot        `json:"snapshot,omitempty"`
	CoverageFacts      *reviewcoverage.Facts  `json:"coverage_facts,omitempty"`
	ID                 string                 `json:"id"`
	Phase              string                 `json:"phase"`
	State              string                 `json:"state"`
	Responses          []ReviewRepairResponse `json:"responses"`
	Repeated           int                    `json:"repeated"`
	Fingerprint        string                 `json:"fingerprint"`
	CandidateMessageID string                 `json:"candidate_message_id"`
	Diagnostics        []api.ToolFeedback     `json:"diagnostics"`
	CreatedAt          time.Time              `json:"created_at"`
	UpdatedAt          time.Time              `json:"updated_at"`
}

func readReviewRepairs(vars map[string]any) ([]ReviewRepair, error) {
	if vars[reviewRepairsKey] == nil {
		return nil, nil
	}
	raw, err := json.Marshal(vars[reviewRepairsKey])
	if err != nil {
		return nil, err
	}
	var rows []ReviewRepair
	if err = json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode review repairs: %w", err)
	}
	return rows, nil
}

func CurrentReviewRepair(vars map[string]any, phase string) (*ReviewRepair, error) {
	rows, err := readReviewRepairs(vars)
	if err != nil {
		return nil, err
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Phase == phase {
			return &rows[i], nil
		}
	}
	return nil, nil
}

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
	run, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || run == nil {
		return err
	}
	unlock := m.lockRunVars(run.ID)
	defer unlock()
	run, err = m.loadRun(ctx, run.ID)
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
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return err
	}
	phase, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok || phase.ReviewLoop == nil {
		return nil
	}
	vars, err := m.Store.GetScaffoldVars(ctx, run.ID)
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
	if !episode.observeResponse(response, msg.ID, reviewIssueFingerprint(result.Feedback)) {
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
			facts := BuildCoverageFacts(manifest, vars, snapshot.Workers, snapshot.Scans)
			episode.CoverageFacts = &facts
		}

		run.Status = api.WorkflowRunStatusPaused
		run.PauseReason = ReviewBlockedReason
		run.PausedAt = &now
		b := newCommandBoundary(run, run.Revision, "paused", run.CurrentPhase, ReviewBlockedReason)
		boundary = &b
	}
	run.UpdatedAt = now
	vars = maps.Clone(vars)
	vars[reviewRepairsKey] = rows
	if err := m.commitCommand(ctx, run, "review_repair", struct{ MessageID string }{msg.ID}, vars, boundary, "", workflowWorkerMutation{HoldPending: blocked}, nil); err != nil {
		return err
	}
	m.publishSession(ctx, run)
	return nil
}

// observeResponse counts a model response once while retaining its latest result.
func (r *ReviewRepair) observeResponse(response, result, fingerprint string) bool {
	for _, attempt := range r.Responses {
		if slices.Contains(attempt.Results, result) {
			return false
		}
	}
	if len(r.Responses) == 0 || r.Responses[len(r.Responses)-1].ID != response {
		if slices.ContainsFunc(r.Responses, func(a ReviewRepairResponse) bool { return a.ID == response }) {
			return false
		}
		r.Responses = append(r.Responses, ReviewRepairResponse{ID: response})
	}
	last := &r.Responses[len(r.Responses)-1]
	last.Results = append(last.Results, result)
	last.Fingerprint = fingerprint
	r.Fingerprint = fingerprint
	r.Repeated = 0
	for i := len(r.Responses) - 1; fingerprint != "" && i >= 0 && r.Responses[i].Fingerprint == fingerprint; i-- {
		r.Repeated++
	}
	return true
}

func repairableVerdictCodes(codes []string) bool {
	for _, code := range codes {
		switch code {
		case "TOOL_ARGS_INVALID", ReviewLoopVerdictInvalidCode, SubmitVerdictInventoryUnaccountedCode, SubmitVerdictScanGroupUnknownCode, "SUBMIT_VERDICT_QUESTION_INVALID", "SUBMIT_VERDICT_CITATIONS_REQUIRED", "SUBMIT_VERDICT_CITATION_UNGROUNDED", "SUBMIT_VERDICT_REVIEWER_UNCITED":
			return true
		}
	}
	return false
}

func reviewIssueFingerprint(feedback []api.ToolFeedback) string {
	var signatures []string
	for _, f := range feedback {
		identity := reviewDiagnosticIdentity(f.Code, f.Details)
		if identity == "" {
			return ""
		}
		signatures = append(signatures, identity)
	}
	if len(signatures) == 0 {
		return ""
	}
	slices.Sort(signatures)
	return reviewcoverage.Identity(signatures)
}

func reviewDiagnosticIdentity(code string, details map[string]any) string {
	// Stable typed fields exclude wording, examples, and candidate text.
	sig := map[string]any{"code": code}
	for _, key := range []string{"field", "field_path", "expected_type", "json_malformed", "json_encoded", "did_you_mean"} {
		if value, ok := details[key]; ok {
			sig[key] = value
		}
	}
	for _, key := range []string{"unknown_groups", "unaccounted_group_ids", "ungrounded_sample", "schema_issues", "conflict_keys", "missing_reviewers"} {
		if value, ok := details[key]; ok {
			sig[key] = canonicalDiagnosticSet(value)
		}
	}
	if raw, ok := details["issues"]; ok {
		var issues []reviewcoverage.Issue
		if decodeDiagnosticRows(raw, &issues) {
			stable := make([]map[string]any, 0, len(issues))
			for _, issue := range issues {
				stable = append(stable, map[string]any{"kind": issue.Kind, "field_path": issue.FieldPath, "fact_id": issue.FactID})
			}
			sig["issues"] = canonicalDiagnosticSet(stable)
		}
	}
	if raw, ok := details["repairs"]; ok {
		var repairs []verdictRepair
		if decodeDiagnosticRows(raw, &repairs) {
			stable := make([]string, 0, len(repairs))
			for _, repair := range repairs {
				identity := reviewDiagnosticIdentity(repair.Code, repair.Details)
				if identity == "" {
					return ""
				}
				stable = append(stable, identity)
			}
			slices.Sort(stable)
			sig["repairs"] = stable
		}
	}
	if len(sig) == 1 {
		return ""
	}
	return reviewcoverage.Identity(sig)
}

// decodeDiagnosticRows reads a feedback detail as typed rows, whether it holds
// in-process values or JSON decoded on recovery. An unreadable detail
// contributes nothing to the identity.
func decodeDiagnosticRows(raw, out any) bool {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return false
	}
	return json.Unmarshal(encoded, out) == nil
}

func canonicalDiagnosticSet(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var rows []json.RawMessage
	if json.Unmarshal(encoded, &rows) != nil {
		return value
	}
	stable := make([]string, len(rows))
	for i, row := range rows {
		stable[i] = reviewcoverage.Identity(row)
	}
	slices.Sort(stable)
	return stable
}

func resolveReviewRepair(vars map[string]any, phase string) (map[string]any, error) {
	rows, err := readReviewRepairs(vars)
	if err != nil {
		return nil, err
	}
	changed := false
	for i := range rows {
		if rows[i].Phase == phase && (rows[i].State == "repairing" || rows[i].State == "blocked") {
			rows[i].State = "resolved"
			rows[i].UpdatedAt = time.Now().UTC()
			changed = true
		}
	}
	if !changed {
		return vars, nil
	}
	next := maps.Clone(vars)
	next[reviewRepairsKey] = rows
	return next, nil
}

// Recover closes the crash window between transcript commit and repair
// accounting. Accepted submissions delimit the current repair sequence.
func (m ReviewRepairs) Recover(ctx context.Context) error {
	if m.Sessions == nil {
		return nil
	}
	runs, err := m.Store.ListRunning(ctx)
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

// ReviewSnapshot freezes the evidence used by an incomplete report at pause.
// Its candidate remains explicitly separate from accepted verdict records.
type ReviewSnapshot struct {
	Vars        map[string]any   `json:"vars"`
	Workers     []api.WorkerTask `json:"workers"`
	Scans       []api.CodeScan   `json:"scans"`
	Verdicts    []PhaseVerdict   `json:"verdicts"`
	Candidate   map[string]any   `json:"candidate,omitempty"`
	Unavailable []string         `json:"unavailable,omitempty"`
}

func (m ReviewRepairs) captureSnapshot(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, vars map[string]any, candidate map[string]any) ReviewSnapshot {
	snapshot := ReviewSnapshot{Vars: map[string]any{}, Candidate: jsonvalue.CloneMap(candidate)}
	// Only report inputs are copied, excluding repair episodes themselves.
	for _, key := range []string{"fanout_plans"} {
		if value, ok := vars[key]; ok {
			snapshot.Vars[key] = value
		}
	}
	if m.WorkerTasks != nil {
		workers, err := m.WorkerTasks(ctx, run.ID)
		if err != nil {
			snapshot.Unavailable = append(snapshot.Unavailable, "worker ledger")
		} else {
			snapshot.Workers = workers
		}
	} else {
		snapshot.Unavailable = append(snapshot.Unavailable, "worker ledger")
	}
	if m.Inventory != nil {
		inventory, err := LoadRunInventory(ctx, m.Inventory, run.ID)
		if err != nil {
			snapshot.Unavailable = append(snapshot.Unavailable, "scan ledger")
		} else {
			snapshot.Scans = inventory.Scans
		}
	} else {
		snapshot.Unavailable = append(snapshot.Unavailable, "scan ledger")
	}
	verdicts, err := ReviewVerdicts(ctx, m.RunManager, run, manifest)
	snapshot.Verdicts = verdicts
	if err != nil {
		snapshot.Unavailable = append(snapshot.Unavailable, "review evidence ledger")
	}
	return snapshot
}

type reviewContractError struct{ error }

const reviewContractInvalidCode = "WORKFLOW_REVIEW_CONTRACT_INVALID"

// blockContract pauses a review whose offered contract cannot accept the
// host's own facts, before any model attempt is spent.
func (m ReviewRepairs) blockContract(ctx context.Context, runID string, cause error) error {
	unlock := m.lockRunVars(runID)
	defer unlock()
	run, err := m.loadRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status != api.WorkflowRunStatusRunning {
		return nil
	}
	vars, err := m.Store.GetScaffoldVars(ctx, runID)
	if err != nil {
		return err
	}
	repairs, err := readReviewRepairs(vars)
	if err != nil {
		return err
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return err
	}
	snapshot := m.captureSnapshot(ctx, run, manifest, vars, nil)
	now := time.Now().UTC()
	repair := ReviewRepair{Snapshot: &snapshot, ID: uuid.NewString(), Phase: run.CurrentPhase, State: "blocked", CreatedAt: now, UpdatedAt: now, Diagnostics: []api.ToolFeedback{{Code: reviewContractInvalidCode, Details: map[string]any{"reason": cause.Error()}}}}
	if len(snapshot.Unavailable) == 0 {
		facts := BuildCoverageFacts(manifest, vars, snapshot.Workers, snapshot.Scans)
		repair.CoverageFacts = &facts
	}
	repairs = append(repairs, repair)

	vars = maps.Clone(vars)
	vars[reviewRepairsKey] = repairs
	run.Status = api.WorkflowRunStatusPaused
	run.PauseReason = ReviewBlockedReason
	run.PausedAt = &now
	run.UpdatedAt = now
	boundary := newCommandBoundary(run, run.Revision, "paused", run.CurrentPhase, ReviewBlockedReason)
	if err := m.commitCommand(ctx, run, "review_contract_blocked", struct{ Code string }{reviewContractInvalidCode}, vars, &boundary, "", workflowWorkerMutation{HoldPending: true}, nil); err != nil {
		return err
	}
	m.publishSession(ctx, run)
	return nil
}
