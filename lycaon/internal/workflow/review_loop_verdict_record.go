package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

type verdictOperationContextKey struct{}

func withVerdictOperationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, verdictOperationContextKey{}, strings.TrimSpace(id))
}

// RecordReviewLoopVerdict applies one structured review result.
func (m *RunManager) RecordReviewLoopVerdict(
	ctx context.Context,
	sessionID string,
	verdict map[string]string,
	citedEvidence []api.CitationGroundingCitedEvidence,
	citedURLs []string,
) (ReviewLoopVerdictOutcome, error) {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return ReviewLoopVerdictOutcome{}, nil
	}
	operationID, _ := ctx.Value(verdictOperationContextKey{}).(string)
	if operationID == "" {
		operationID = uuid.NewString()
	}
	inputRaw, err := json.Marshal(VerdictSubmission{SessionID: sessionID, Verdict: verdict, Cited: citedEvidence, CitedURLs: citedURLs})
	if err != nil {
		return ReviewLoopVerdictOutcome{}, err
	}
	digest := sha256.Sum256(inputRaw)
	inputDigest := hex.EncodeToString(digest[:])
	replay, err := m.resolveVerdictReplay(ctx, operationID, inputDigest)
	if err != nil {
		return ReviewLoopVerdictOutcome{}, err
	}
	if replay.Replayed {
		return replay.Outcome, nil
	}
	target, err := m.resolveVerdictTarget(ctx, sessionID, operationID, replay.Pending)
	if err != nil {
		return ReviewLoopVerdictOutcome{}, err
	}
	if target.Converged {
		return ReviewLoopVerdictOutcome{}, nil
	}
	active, rl, key := target.Run, target.Loop, target.Key
	unlockVars := m.lockRunVars(active.ID)
	varsUnlocked := false
	defer func() {
		if !varsUnlocked {
			unlockVars()
		}
	}()
	vars, err := m.Store.GetScaffoldVars(ctx, active.ID)
	if err != nil {
		return ReviewLoopVerdictOutcome{}, err
	}

	out := ReviewLoopVerdictOutcome{
		Applied:     true,
		Phase:       active.CurrentPhase,
		EvidenceKey: key,
	}
	rules, err := m.VerdictRulesFor(ctx, active)
	if err != nil {
		return ReviewLoopVerdictOutcome{}, err
	}
	out.Valid = ValidateReviewLoopVerdict(rl, verdict, rules) == nil
	if out.Valid && ReviewLoopVerdictTerminal(rl, verdict) {
		out.InventoryIssue, err = m.checkReviewInventory(ctx, active, rl, verdict)
		if err != nil {
			return out, err
		}
		if out.InventoryIssue != nil {
			out.Valid = false
		}
	}
	if out.Valid && ReviewLoopVerdictTerminal(rl, verdict) {
		owed, captured := effectiveReviewAgents(active.CurrentPhase, rl, vars)
		if !captured {
			return out, fmt.Errorf("reviewer roster unavailable for phase %q", active.CurrentPhase)
		}
		if missing := m.missingReviewAgents(ctx, sessionID, owed); len(missing) > 0 {
			out.Valid = false
			out.MissingAgents = missing
		} else if m.VerdictGrounding != nil {
			eval, evalErr := m.VerdictGrounding(ctx, sessionID, allVerdictCitations(rl, verdict, citedEvidence), citedURLs, owed)
			if evalErr != nil {
				return out, evalErr
			}
			if eval.Code != "" {
				out.Valid = false
				out.GroundingCode = eval.Code
				out.UngroundedCount = eval.UngroundedCount
				out.UngroundedSample = eval.UngroundedSample
				out.UncitedReviewers = eval.UncitedReviewers
				out.ObservedHandles = eval.ObservedHandles
			} else {
				out.Grounding = eval.Grounding
			}
		}
	}
	var committedVars map[string]any
	attemptSoFar := ReviewLoopAttempt(vars, active.CurrentPhase)
	switch {
	case out.Valid && ReviewLoopVerdictTerminal(rl, verdict):
		out.Terminal = true
		out.Attempt = attemptSoFar
		committedVars = StampReviewVerdict(vars, key, verdict)
		committedVars = SatisfyGateInVars(committedVars, "evidence_passed:"+key)
	case out.Valid && attemptSoFar >= reviewLoopIterationCap(rl):
		// At the cap a non-terminal verdict is rejected rather than opening another round.
		out.Valid = false
		out.IterationCapExceeded = true
		out.Attempt = attemptSoFar
	case out.Valid:
		committedVars = bumpReviewLoopAttempt(vars, active.CurrentPhase)
		out.Attempt = ReviewLoopAttempt(committedVars, active.CurrentPhase)
	}
	evidenceID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("verdict-evidence:"+operationID)).String()
	op := verdictOperation{
		ToolCallID: operationID, RunID: active.ID, SourceRevision: active.Revision,
		Phase: active.CurrentPhase, InputDigest: inputDigest, EvidenceRecordID: evidenceID, EvidenceJSON: string(inputRaw),
	}
	stored, _, prepareErr := m.Store.prepareVerdictOperation(ctx, op)
	if prepareErr != nil {
		return out, prepareErr
	}
	op = *stored
	if op.Status == "prepared" && out.Valid {
		// Validated provenance supersedes the raw citation args once the audit ran.
		persistCited := citedEvidence
		if out.Grounding != nil && len(out.Grounding.CitedEvidence) > 0 {
			persistCited = out.Grounding.CitedEvidence
		}
		if err := m.persistReviewLoopEvidence(ctx, sessionID, active, rl, target.PhaseID, verdict, persistCited, citedURLs, evidenceID, op.CreatedAt); err != nil {
			return out, err
		}
	}
	if op.Status == "prepared" {
		if err := m.Store.markVerdictEvidenceApplied(ctx, operationID); err != nil {
			return out, err
		}
	}
	projectDir := m.projectDirForRun(ctx, active)
	if err := m.Store.commitVerdictOperation(ctx, op, active, projectDir, committedVars, out); err != nil {
		return out, err
	}
	unlockVars()
	varsUnlocked = true
	if out.Terminal {
		_, err = m.TryAutoAdvance(ctx, active.ID)
		return out, err
	}
	if !out.Valid {
		// Only an exceeded iteration cap requests a decision.
		m.notifyReviewLoopHeld(ctx, sessionID, out.IterationCapExceeded)
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
	Outcome  ReviewLoopVerdictOutcome
	Replayed bool
	Pending  *verdictOperation
}

func (m *RunManager) resolveVerdictReplay(ctx context.Context, operationID, inputDigest string) (verdictReplay, error) {
	existing, ok, err := m.Store.getVerdictOperation(ctx, operationID)
	if err != nil || !ok {
		return verdictReplay{}, err
	}
	if existing.InputDigest != inputDigest {
		return verdictReplay{}, fmt.Errorf("submit_verdict tool call %s: %w", operationID, ErrOperationConflict)
	}
	switch {
	case existing.Status == "committed":
		var replayed ReviewLoopVerdictOutcome
		if err := json.Unmarshal([]byte(existing.ResponseJSON), &replayed); err != nil {
			return verdictReplay{}, err
		}
		if replayed.Terminal {
			_, _ = m.TryAutoAdvance(ctx, existing.RunID)
		}
		return verdictReplay{Outcome: replayed, Replayed: true}, nil
	case existing.Status == "diverged":
		return verdictReplay{}, fmt.Errorf("submit_verdict tool call %s can no longer apply: %s", operationID, existing.Error)
	case verdictOperationPending(existing.Status):
		return verdictReplay{Pending: existing}, nil
	}
	return verdictReplay{}, nil
}

// verdictTarget identifies the active review loop for a verdict.
type verdictTarget struct {
	Run       *api.WorkflowRun
	Loop      workflowdef.ReviewLoopDef
	PhaseID   string
	Key       string
	Converged bool
}

func (m *RunManager) resolveVerdictTarget(
	ctx context.Context,
	sessionID string,
	operationID string,
	pending *verdictOperation,
) (verdictTarget, error) {
	converge := func(reason string) (verdictTarget, error) {
		if pending == nil {
			return verdictTarget{Converged: true}, nil
		}
		return verdictTarget{Converged: true}, m.Store.resolveVerdictOperationDiverged(ctx, operationID, reason)
	}
	active, err := m.Store.ActiveBySession(ctx, sessionID)
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
		if err := m.Store.rebaseVerdictOperation(ctx, operationID, active.Revision); err != nil {
			return verdictTarget{}, err
		}
	}
	manifest, err := m.manifestForRun(ctx, active)
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
	return verdictTarget{Run: active, Loop: rl, PhaseID: def.ID, Key: key}, nil
}

// ReviewLoopVerdictOutcome reports what RecordReviewLoopVerdict did, for tool results.
type ReviewLoopVerdictOutcome struct {
	// Applied reports whether the active phase accepts review verdicts.
	Applied bool
	// Valid reports schema validity against the phase verdict_schema.
	Valid bool
	// Terminal reports the gate was satisfied and the host is advancing.
	Terminal bool
	// Attempt is the review round counter after this submission.
	Attempt     int
	Phase       string
	EvidenceKey string
	// MissingAgents lists required reviewers without succeeded envelopes.
	MissingAgents  []string
	InventoryIssue *guidance.ReportDocumentIssue
	// IterationCapExceeded reports a non-terminal verdict rejected because the
	// phase already reached iteration_cap on a prior attempt.
	IterationCapExceeded bool
	// GroundingCode is the citation-audit reject on a terminal verdict.
	GroundingCode    string
	UngroundedCount  int
	UngroundedSample []string
	UncitedReviewers []string
	ObservedHandles  []string
	// Grounding is the validated citation provenance of an accepted terminal verdict.
	Grounding *api.CitationGrounding
}

// allVerdictCitations flattens the top-level citation channel with every
// claims-typed field's per-claim citations for one grounding audit.
func allVerdictCitations(
	rl workflowdef.ReviewLoopDef,
	verdict map[string]string,
	cited []api.CitationGroundingCitedEvidence,
) []api.CitationGroundingCitedEvidence {
	out := append([]api.CitationGroundingCitedEvidence(nil), cited...)
	claims, err := ParseVerdictClaims(rl, verdict)
	if err != nil {
		return out
	}
	for _, list := range claims {
		for _, claim := range list {
			out = append(out, claim.CitedEvidence...)
		}
	}
	return out
}

func (m *RunManager) persistReviewLoopEvidence(
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
	attempt := 0
	if vars, err := m.Store.GetScaffoldVars(ctx, active.ID); err == nil {
		attempt = ReviewLoopAttempt(vars, phaseID)
	}
	rec := evidence.GateRecord(
		evidence.GateType(strings.TrimSpace(rl.EvidenceKey)),
		phaseID,
		active.ID,
		ReviewLoopVerdictEvidenceVerdict(rl, verdict),
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
	if rl.IterationCap > 0 {
		return rl.IterationCap
	}
	return defaultReviewLoopIterationCap
}

// missingReviewAgents lists required_agents without a succeeded envelope in the current sojourn.
func (m *RunManager) missingReviewAgents(ctx context.Context, sessionID string, required []string) []string {
	if len(required) == 0 || m == nil || m.Sessions == nil {
		return nil
	}
	msgs, err := m.Sessions.GetMessages(ctx, sessionID)
	if err != nil {
		return append([]string(nil), required...)
	}
	present := map[string]struct{}{}
	for _, msg := range msgs[api.UserIntentBoundary(msgs):] {
		if msg.WorkerSummary == nil {
			continue
		}
		agent := strings.TrimSpace(msg.WorkerSummary.AgentType)
		if agent == "" {
			continue
		}
		if api.WorkerSummaryLegSucceeded(msg.WorkerSummary.Status) {
			present[agent] = struct{}{}
		}
	}
	var missing []string
	for _, agent := range required {
		agent = strings.TrimSpace(agent)
		if agent == "" {
			continue
		}
		if _, ok := present[agent]; !ok {
			missing = append(missing, agent)
		}
	}
	return missing
}

func (m *RunManager) notifyReviewLoopHeld(ctx context.Context, sessionID string, decisionRequired bool) {
	if m != nil && m.OnReviewLoopHeld != nil {
		m.OnReviewLoopHeld(ctx, sessionID, decisionRequired)
	}
}

// ReviewLoopAttempt returns the phase's recorded non-terminal attempt count, 0 when none.
func ReviewLoopAttempt(vars map[string]any, phaseID string) int {
	if s, ok := DotPathString(vars, reviewLoopAttemptPath(phaseID)); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return n
		}
	}
	return 0
}

func bumpReviewLoopAttempt(vars map[string]any, phaseID string) map[string]any {
	return SetHostVar(vars, reviewLoopAttemptPath(phaseID), strconv.Itoa(ReviewLoopAttempt(vars, phaseID)+1))
}

func reviewLoopAttemptPath(phaseID string) string {
	return "review_loop." + phaseID + ".attempt"
}
