package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/toolrejection"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

type verdictOperationContextKey struct{}

func WithOperationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, verdictOperationContextKey{}, strings.TrimSpace(id))
}

// RecordReviewLoopVerdict applies one structured review result.
func (m *Verdicts) RecordReviewLoopVerdict(
	ctx context.Context,
	sessionID string,
	verdict map[string]string,
	citedEvidence []api.CitationGroundingCitedEvidence,
	citedURLs []string,
) (runstate.ReviewOutcome, error) {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return runstate.ReviewOutcome{}, nil
	}
	operationID, _ := ctx.Value(verdictOperationContextKey{}).(string)
	if operationID == "" {
		operationID = uuid.NewString()
	}
	inputRaw, err := json.Marshal(runstate.VerdictSubmission{SessionID: sessionID, Verdict: verdict, Cited: citedEvidence, CitedURLs: citedURLs})
	if err != nil {
		return runstate.ReviewOutcome{}, err
	}
	digest := sha256.Sum256(inputRaw)
	inputDigest := hex.EncodeToString(digest[:])
	replay, err := m.resolveVerdictReplay(ctx, operationID, inputDigest)
	if err != nil {
		return runstate.ReviewOutcome{}, err
	}
	if replay.Replayed {
		return replay.Outcome, nil
	}
	target, err := m.resolveVerdictTarget(ctx, sessionID, operationID, replay.Pending)
	if err != nil {
		return runstate.ReviewOutcome{}, err
	}
	if target.Converged {
		return runstate.ReviewOutcome{}, nil
	}
	active, rl, key := target.Run, target.Loop, target.Key
	unlockVars := m.Vars.Lock(active.ID)
	varsUnlocked := false
	defer func() {
		if !varsUnlocked {
			unlockVars()
		}
	}()
	vars, err := m.Runs.GetScaffoldVars(ctx, active.ID)
	if err != nil {
		return runstate.ReviewOutcome{}, err
	}

	checked, err := m.validateReviewSubmission(ctx, active, rl, verdict, vars, citedEvidence, citedURLs)
	if err != nil {
		return runstate.ReviewOutcome{}, err
	}
	out, questionVars := checked.Outcome, checked.Vars
	var committedVars map[string]any
	attemptSoFar := runstate.ReviewLoopAttempt(vars, active.CurrentPhase)
	switch {
	case out.Valid && workflowvalidation.ReviewLoopVerdictTerminal(rl, verdict):
		out.Terminal = true
		out.Attempt = attemptSoFar
		committedVars = runstate.StampReviewVerdict(questionVars, key, verdict)
		committedVars = runstate.SatisfyGateInVars(committedVars, "evidence_passed:"+key)
	case out.Valid && rl.FollowupAttempts > 0:
		committedVars = questionVars
		out.Attempt = attemptSoFar
	case out.Valid && attemptSoFar >= reviewLoopIterationCap(rl):
		// Exhausted review rounds retain the current phase.
		out.Valid = false
		out.IterationCapExceeded = true
		out.Attempt = attemptSoFar
	case out.Valid:
		committedVars = runstate.BumpReviewLoopAttempt(vars, active.CurrentPhase)
		out.Attempt = runstate.ReviewLoopAttempt(committedVars, active.CurrentPhase)
	}
	if out.Valid && committedVars != nil {
		committedVars, err = runstate.ResolveReviewRepair(committedVars, active.CurrentPhase)
		if err != nil {
			return out, err
		}
	}
	evidenceID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("verdict-evidence:"+operationID)).String()
	op := runstate.VerdictOperation{
		ToolCallID: operationID, RunID: active.ID, SourceRevision: active.Revision,
		Phase: active.CurrentPhase, InputDigest: inputDigest, EvidenceRecordID: evidenceID, EvidenceJSON: string(inputRaw),
	}
	stored, _, prepareErr := m.Records.PrepareVerdictOperation(ctx, op)
	if prepareErr != nil {
		return out, prepareErr
	}
	op = *stored
	projectDir := m.Resolver.ProjectDirForRun(ctx, active)
	if err := m.Records.CommitVerdictOperation(ctx, op, active, projectDir, committedVars, out); err != nil {
		if rejection := toolrejection.AsToolReject(err); rejection != nil && rejection.Code == ReviewContextChangedCode {
			if resolveErr := m.Records.ResolveVerdictOperationDiverged(ctx, operationID, rejection.Code); resolveErr != nil {
				return out, resolveErr
			}
			out.Valid, out.Terminal, out.CoverageIssue = false, false, rejection
			return out, nil
		}
		return out, err
	}
	op.Status = "committed"
	if err := m.publishVerdictEvidence(ctx, op, out); err != nil {
		return out, err
	}
	unlockVars()
	varsUnlocked = true
	if out.Terminal {
		_, err = m.Phases.TryAutoAdvance(ctx, active.ID)
		return out, err
	}
	if !out.Valid {
		// Only an exceeded iteration cap requests a decision.
		if out.IterationCapExceeded {
			m.notifyReviewLoopHeld(ctx, sessionID, true)
		}
		return out, nil
	}
	if rl.FollowupAttempts > 0 {
		return out, nil
	}
	switch {
	case out.Attempt < reviewLoopIterationCap(rl):
		m.notifyReviewLoopHeld(ctx, sessionID, false)
	case out.Attempt == reviewLoopIterationCap(rl):
		m.notifyReviewLoopHeld(ctx, sessionID, true)
	}
	return out, nil
}

// verdictReplay carries a committed result or resumable receipt.
type verdictReplay struct {
	Outcome  runstate.ReviewOutcome
	Replayed bool
	Pending  *runstate.VerdictOperation
}

func (m *Verdicts) resolveVerdictReplay(ctx context.Context, operationID, inputDigest string) (verdictReplay, error) {
	existing, ok, err := m.Records.GetVerdictOperation(ctx, operationID)
	if err != nil || !ok {
		return verdictReplay{}, err
	}
	if existing.InputDigest != inputDigest {
		return verdictReplay{}, fmt.Errorf("submit_verdict tool call %s: %w", operationID, runstate.ErrOperationConflict)
	}
	switch {
	case existing.Status == "committed":
		var replayed runstate.ReviewOutcome
		if err := json.Unmarshal([]byte(existing.ResponseJSON), &replayed); err != nil {
			return verdictReplay{}, err
		}
		if err := m.publishVerdictEvidence(ctx, *existing, replayed); err != nil {
			return verdictReplay{}, err
		}
		if replayed.Terminal {
			_, _ = m.Phases.TryAutoAdvance(ctx, existing.RunID)
		}
		return verdictReplay{Outcome: replayed, Replayed: true}, nil
	case existing.Status == "diverged":
		return verdictReplay{}, fmt.Errorf("submit_verdict tool call %s can no longer apply: %s", operationID, existing.Error)
	case runstate.VerdictOperationPending(existing.Status):
		return verdictReplay{Pending: existing}, nil
	}
	return verdictReplay{}, nil
}

// verdictTarget identifies the active review loop for a verdict.
type verdictTarget struct {
	Run       *api.WorkflowRun
	Loop      workflowdef.ReviewLoopDef
	Key       string
	Converged bool
}

func (m *Verdicts) resolveVerdictTarget(
	ctx context.Context,
	sessionID string,
	operationID string,
	pending *runstate.VerdictOperation,
) (verdictTarget, error) {
	converge := func(reason string) (verdictTarget, error) {
		if pending == nil {
			return verdictTarget{Converged: true}, nil
		}
		return verdictTarget{Converged: true}, m.Records.ResolveVerdictOperationDiverged(ctx, operationID, reason)
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil {
		return verdictTarget{}, err
	}
	if active == nil {
		return converge("session has no active workflow run")
	}
	if pending != nil && (pending.RunID != active.ID || pending.Phase != active.CurrentPhase) {
		return converge(fmt.Sprintf("prepared for run %s phase %s; active is run %s phase %s",
			pending.RunID, pending.Phase, active.ID, active.CurrentPhase))
	}
	if pending != nil && pending.SourceRevision != active.Revision {
		// Rebase a prepared verdict after same-phase revision changes.
		if err := m.Records.RebaseVerdictOperation(ctx, operationID, active.Revision); err != nil {
			return verdictTarget{}, err
		}
	}
	manifest, err := m.Resolver.ForRun(ctx, active)
	if err != nil {
		return verdictTarget{}, err
	}
	def, ok := manifest.PhaseByID(active.CurrentPhase)
	if !ok || def.ReviewLoop == nil {
		return converge("phase " + active.CurrentPhase + " no longer carries a review loop")
	}
	rl := *def.ReviewLoop
	key := strings.TrimSpace(rl.EvidenceKey)
	if key == "" {
		return converge("phase " + active.CurrentPhase + " review loop has no evidence key")
	}
	return verdictTarget{Run: active, Loop: rl, Key: key}, nil
}

// allVerdictCitations flattens the top-level citation channel with every
// claims-typed field's per-claim citations for one grounding audit.
func allVerdictCitations(
	rl workflowdef.ReviewLoopDef,
	verdict map[string]string,
	cited []api.CitationGroundingCitedEvidence,
) []api.CitationGroundingCitedEvidence {
	out := append([]api.CitationGroundingCitedEvidence(nil), cited...)
	claims, err := workflowvalidation.ParseVerdictClaims(rl, verdict)
	if err != nil {
		return out
	}
	for _, list := range claims {
		for _, claim := range list {
			out = append(out, claim.CitedEvidence...)
		}
	}
	if review, err := workflowvalidation.ParseVerdictCoverage(rl, verdict); err == nil && review != nil {
		for _, assessment := range review.Assessments {
			out = append(out, assessment.CitedEvidence...)
		}
	}
	return out
}

func (m *Verdicts) persistReviewLoopEvidence(
	ctx context.Context,
	sessionID string,
	active *api.WorkflowRun,
	rl workflowdef.ReviewLoopDef,
	phaseID string,
	verdict map[string]string,
	citedEvidence []api.CitationGroundingCitedEvidence,
	citedURLs []string,
	recordID string,
	recordedAt time.Time,
	attempt int,
) error {
	if m == nil || m.EvidenceStore == nil {
		return nil
	}
	// Citations anchor durable review evidence.
	if len(citedEvidence) == 0 {
		return nil
	}
	projectDir := ""
	if m.EvidenceProjectDir != nil {
		dir, err := m.EvidenceProjectDir(ctx, sessionID)
		if err != nil {
			return err
		}
		projectDir = dir
	}
	if strings.TrimSpace(projectDir) == "" {
		return nil
	}
	artifacts := verdictArtifacts(verdict, citedEvidence, citedURLs)
	rec := evidence.GateRecord(
		evidence.GateType(strings.TrimSpace(rl.EvidenceKey)),
		phaseID,
		active.ID,
		workflowvalidation.ReviewLoopVerdictEvidenceVerdict(rl, verdict),
		strings.TrimSpace(verdict["verdict"]),
		artifacts,
		"",
		"",
		"",
		attempt,
		recordedAt.UTC(),
	)
	rec.RecordID = recordID
	if err := m.EvidenceStore.Append(ctx, projectDir, rec); err != nil {
		return err
	}
	if m.OnGateEvidencePersisted != nil {
		m.OnGateEvidencePersisted(ctx, sessionID, active.ID, rec)
	}
	return nil
}

func verdictArtifacts(verdict map[string]string, cited []api.CitationGroundingCitedEvidence, citedURLs []string) map[string]any {
	out := make(map[string]any, len(verdict)+2)
	for k, v := range verdict {
		out[k] = v
	}
	citedAny := make([]any, 0, len(cited))
	for _, c := range cited {
		citedAny = append(citedAny, map[string]any{
			"handle":  c.Handle,
			"path":    c.Path,
			"line":    c.Line,
			"excerpt": c.Excerpt,
		})
	}
	out[evidence.CitedEvidenceArtifactKey] = citedAny
	urls := make([]any, 0, len(citedURLs))
	for _, u := range citedURLs {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	out[evidence.CitedURLsArtifactKey] = urls
	return out
}

// defaultReviewLoopIterationCap bounds NEEDS_* re-loops when a phase omits iteration_cap.
const defaultReviewLoopIterationCap = 2

func reviewLoopIterationCap(rl workflowdef.ReviewLoopDef) int {
	if rl.FollowupAttempts > 0 {
		return 0
	}
	if rl.IterationCap > 0 {
		return rl.IterationCap
	}
	return defaultReviewLoopIterationCap
}

// Explicit runs own phase reviews; ambient runs use the current user intent.
func (m *Verdicts) missingReviewAgents(ctx context.Context, run *api.WorkflowRun, required []string) []string {
	if len(required) == 0 {
		return nil
	}
	if m == nil || m.WorkerTasks == nil {
		return append([]string(nil), required...)
	}
	tasks, err := m.WorkerTasks(ctx, run.ID)
	if err != nil {
		return append([]string(nil), required...)
	}
	var since time.Time
	ambient := runstate.IsAmbientRun(run)
	if ambient {
		if m.Sessions == nil {
			return append([]string(nil), required...)
		}
		history, err := m.Sessions.GetMessages(ctx, run.SessionID)
		if err != nil {
			return append([]string(nil), required...)
		}
		if boundary := api.UserIntentBoundary(history); boundary > 0 {
			since = history[boundary-1].CreatedAt
		}
	}
	present := map[string]bool{}
	for _, task := range tasks {
		inScope := task.WorkflowRunID == run.ID && task.WorkflowPhase == run.CurrentPhase
		if ambient {
			inScope = !task.CreatedAt.Before(since)
		}
		if inScope && api.WorkerReviewSucceeded(task) {
			present[task.AgentType] = true
		}
	}
	var missing []string
	for _, agent := range required {
		if !present[agent] {
			missing = append(missing, agent)
		}
	}
	return missing
}

func (m *Verdicts) notifyReviewLoopHeld(ctx context.Context, sessionID string, decisionRequired bool) {
	if m != nil && m.OnReviewLoopHeld != nil {
		m.OnReviewLoopHeld(ctx, sessionID, decisionRequired)
	}
}

// runstate.ReviewLoopAttempt returns the phase's recorded non-terminal attempt count, 0 when none.
