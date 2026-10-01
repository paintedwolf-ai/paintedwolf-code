package workercompletion

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	// WorkerSummaryNoArtifactCode blocks an implementer summary with no disk artifact proof.
	WorkerSummaryNoArtifactCode     = "WORKER_SUMMARY_NO_ARTIFACT"
	workerScoutNoSurveyEvidenceCode = "WORKER_SCOUT_NO_SURVEY_EVIDENCE"
)

// WorkerSummaryNoProseCode marks a missing parent survey.
const WorkerSummaryNoProseCode = "WORKER_TURN_NO_PROSE"

// WorkerSummaryEvalInput is input for post-worker summary validation.
type WorkerSummaryEvalInput struct {
	AgentType      string
	MissingReport  bool
	MaxChars       int
	Report         WorkerCompletionReport
	ChildSessionID string
	ChildMessages  []api.Message
	ProjectDir     string
	// ProjectRoots are the session's folder roots for @label citation keys.
	ProjectRoots   []projectroot.RootRef
	ActiveRootID   string
	WorkspaceCheck WorkspaceChangeChecker
	Ledger         guidance.EvidenceLedgerReader
	// Hints is the loaded guidance registry. The audit reads evidence metadata
	// (UI label / check id) from it; nil falls back to a humanized code.
	Hints *guidance.HintConfig
	// Pipeline is required for blocking Decisions (worker.report_check EvaluateBlock).
	Pipeline *oar.GuardPipeline
}

// WorkerSummaryEvalResult is the outcome of summary validation.
type WorkerSummaryEvalResult struct {
	Status     string
	Summary    string
	HintCode   string
	HintData   map[string]any
	HintCopy   map[string]string
	HintEffect oar.Effect
	Grounding  api.CitationGrounding
}

// workerSummaryObs is a blocking observation for worker.report_check.
type workerSummaryObs struct {
	Offenders              []string
	NoProse                bool
	MissingReport          bool
	ActualChars            int
	MaxChars               int
	NoSurveyEvidence       bool
	SurfaceClaimUngrounded bool
	PageMeasureUngrounded  bool
	UnobservedCitedHandles []string
	UnobservedCitedURLs    []string
	NoArtifact             bool
	RejectDataCodes        []string // PutRejectData keys for render vars only
}

// EvaluateWorkerSummary validates worker completion report fields against child tool evidence.
func EvaluateWorkerSummary(ctx context.Context, in WorkerSummaryEvalInput) (WorkerSummaryEvalResult, error) {
	audit, err := newCitationGroundingAudit(ctx, in)
	if err != nil {
		return WorkerSummaryEvalResult{}, fmt.Errorf("load worker evidence ledger: %w", err)
	}
	report := in.Report
	report.Normalize()
	brief := strings.TrimSpace(report.Brief)
	obs := workerSummaryObs{MissingReport: in.MissingReport, NoProse: brief == "" && !in.MissingReport}
	if obs.MissingReport {
		obs.RejectDataCodes = append(obs.RejectDataCodes, WorkerCompletionReportMissingCode)
	}
	if in.MaxChars > 0 && utf8.RuneCountInString(brief) > in.MaxChars {
		obs.ActualChars, obs.MaxChars = utf8.RuneCountInString(brief), in.MaxChars
		obs.RejectDataCodes = append(obs.RejectDataCodes, "WORKER_SUMMARY_TOO_LONG")
	}
	if obs.MissingReport || obs.NoProse {
		return decideWorkerSummary(ctx, in, &audit, brief, obs, report)
	}

	if prompts.AgentIsReadScout(in.AgentType) && strings.EqualFold(strings.TrimSpace(report.LegStatus), "complete") {
		if !audit.recordScoutSurveyCheck() {
			obs.NoSurveyEvidence = true
			obs.RejectDataCodes = append(obs.RejectDataCodes, workerScoutNoSurveyEvidenceCode)
		}
	}

	if code, offenders := guidance.EvaluateSurfaceClaimComplete(report.LegStatus, audit.ev); code != "" {
		obs.Offenders = append(obs.Offenders, offenders...)
		obs.SurfaceClaimUngrounded = true
		obs.RejectDataCodes = append(obs.RejectDataCodes, code)
	}

	citationEval := guidance.EvaluateWorkerCitations(
		evidence.CitationRoots{
			ProjectDir:   in.ProjectDir,
			Roots:        in.ProjectRoots,
			ActiveRootID: in.ActiveRootID,
		},
		workerFindingsInput(report.Findings),
		report.CitedURLs,
		workerNarrativeInput(report),
		audit.ev,
	)
	if citationEval.Code != "" {
		if offenders, ok := audit.recordTypedCitationCheck(citationEval); !ok {
			obs.Offenders = append(obs.Offenders, offenders...)
			obs.RejectDataCodes = append(obs.RejectDataCodes, citationEval.Code)
			switch citationEval.Code {
			case guidance.SurfaceClaimUngroundedCode:
				obs.SurfaceClaimUngrounded = true
			case guidance.PageMeasureUngroundedCode:
				obs.PageMeasureUngrounded = true
			case guidance.WorkerURLNotObservedCode:
				obs.UnobservedCitedURLs = append([]string(nil), offenders...)
			default:
				obs.UnobservedCitedHandles = append([]string(nil), offenders...)
			}
		}
	}
	audit.recordFindingCitationVerdicts(citationEval)
	audit.recordTypedCitationChannels(report)
	if citationEval.Code == "" {
		audit.recordBindAdvisories(citationEval)
		audit.recordSurveyAdvisories(citationEval)
		audit.recordProseDuplication(citationEval)
		audit.recordProseAdvisories(citationEval)
	}

	if prompts.AgentIsWebResearcher(in.AgentType) && len(report.CitedURLs) == 0 {
		audit.passVacuous("url_citations", "URL citations", "No URL citations in report")
	}

	if isImplementerAgent(in.AgentType) {
		proof := childHadImplementerArtifact(ctx, in.ProjectDir, in.ChildMessages, in.WorkspaceCheck)
		if !audit.recordImplementerArtifactCheck(proof) {
			obs.NoArtifact = true
			obs.RejectDataCodes = append(obs.RejectDataCodes, WorkerSummaryNoArtifactCode)
		}
	}

	return decideWorkerSummary(ctx, in, &audit, brief, obs, report)
}

// decideWorkerSummary publishes facts and lets EvaluateBlock decide.
func decideWorkerSummary(
	ctx context.Context,
	in WorkerSummaryEvalInput,
	audit *citationGroundingAudit,
	summary string,
	obs workerSummaryObs,
	report WorkerCompletionReport,
) (WorkerSummaryEvalResult, error) {
	data := guidance.OffenderHintData(obs.Offenders)
	if audit != nil {
		data = guidance.GroundingHintData(obs.Offenders, audit.ev)
	}
	observedValid := !obs.MissingReport && !obs.NoProse && !obs.NoSurveyEvidence && !obs.SurfaceClaimUngrounded && !obs.PageMeasureUngrounded && !obs.NoArtifact && len(obs.Offenders) == 0 && obs.MaxChars == 0
	if obs.MaxChars > 0 {
		data["actual_chars"], data["max_chars"] = obs.ActualChars, obs.MaxChars
	}
	if in.Pipeline == nil || !in.Pipeline.AnchorEnforced(oar.AnchorWorkerReportCheck) {
		return WorkerSummaryEvalResult{Status: "complete", Summary: summary, Grounding: audit.finish(observedValid, "", report)}, nil
	}
	gc := oar.NewGuardContext()
	gc.SessionID = in.ChildSessionID
	gc.Profile = "worker"
	publishWorkerSummaryFacts(gc, in.AgentType, obs)
	putWorkerRejectData(gc, obs, data)
	var code string
	var effect oar.Effect
	var copy map[string]string
	res, err := in.Pipeline.EvaluateBlock(ctx, oar.AnchorWorkerReportCheck, gc)
	if err != nil {
		return WorkerSummaryEvalResult{}, fmt.Errorf("evaluate worker.report_check: %w", err)
	}
	if res != nil && res.Decision != nil {
		d := res.Decision
		if d.Effect == oar.EffectBlock || d.Effect == oar.EffectNudge {
			code, copy, effect = d.Code, d.Copy, d.Effect
			if d.Data != nil {
				data = d.Data
			}
		}
	}
	status := "complete"
	if code != "" {
		status = "partial"
	}
	result := WorkerSummaryEvalResult{Status: status, Summary: summary, HintCode: code, HintData: data, HintCopy: copy, HintEffect: effect}
	if audit != nil {
		result.Grounding = audit.finish(observedValid && code == "", code, report)
	}
	return result, nil
}

// publishWorkerSummaryFacts sets worker.report_check observations.
func publishWorkerSummaryFacts(gc *oar.GuardContext, agentType string, obs workerSummaryObs) {
	if gc == nil {
		return
	}
	gc.AgentIsScout = prompts.AgentIsReadScout(agentType)
	if fetches, ok := prompts.AgentFetchesURLs(agentType); ok {
		gc.ProfileFetchesURLs = fetches
	}
	gc.AgentIsImplementer = isImplementerAgent(agentType)
	gc.WorkerSummaryPresent = !obs.NoProse && !obs.MissingReport
	if obs.MissingReport {
		gc.Missing = true
	}
	if obs.MaxChars > 0 {
		gc.RejectObservation = "summary_too_long"
	}
	gc.ScoutSurveyEvidencePresent = !obs.NoSurveyEvidence
	gc.WorkerArtifactPresent = !obs.NoArtifact
	gc.SurfaceClaimUngrounded = obs.SurfaceClaimUngrounded
	gc.PageMeasureUngrounded = obs.PageMeasureUngrounded
	if len(obs.UnobservedCitedHandles) > 0 {
		gc.UnobservedCitedHandles = append([]string(nil), obs.UnobservedCitedHandles...)
	}
	if len(obs.UnobservedCitedURLs) > 0 {
		gc.UnobservedCitedURLs = append([]string(nil), obs.UnobservedCitedURLs...)
	}
}

func putWorkerRejectData(gc *oar.GuardContext, obs workerSummaryObs, data map[string]any) {
	if gc == nil {
		return
	}
	seen := map[string]bool{}
	for _, code := range obs.RejectDataCodes {
		code = strings.TrimSpace(code)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		gc.PutRejectData(code, data)
	}
	if obs.NoProse {
		gc.PutRejectData(WorkerSummaryNoProseCode, data)
	}
	if obs.NoSurveyEvidence {
		gc.PutRejectData(workerScoutNoSurveyEvidenceCode, data)
	}
	if obs.SurfaceClaimUngrounded {
		gc.PutRejectData(guidance.SurfaceClaimUngroundedCode, data)
	}
	if obs.PageMeasureUngrounded {
		gc.PutRejectData(guidance.PageMeasureUngroundedCode, data)
	}
	if len(obs.UnobservedCitedHandles) > 0 {
		gc.PutRejectData(guidance.WorkerEvidenceHandleUnknownCode, data)
	}
	if len(obs.UnobservedCitedURLs) > 0 {
		gc.PutRejectData(guidance.WorkerURLNotObservedCode, data)
	}
	if obs.NoArtifact {
		gc.PutRejectData(WorkerSummaryNoArtifactCode, data)
	}
}

func workerNarrativeInput(report WorkerCompletionReport) guidance.WorkerNarrativeInput {
	notes := make([]string, 0, len(report.Findings))
	for _, f := range report.Findings {
		notes = append(notes, f.Note)
	}
	return guidance.WorkerNarrativeInput{
		Brief:         report.Brief,
		ObjectivesMet: report.ObjectivesMet,
		RemainingRisk: report.RemainingRisk,
		FindingNotes:  notes,
	}
}

func workerFindingsInput(findings []WorkerFinding) []guidance.WorkerFindingInput {
	if len(findings) == 0 {
		return nil
	}
	out := make([]guidance.WorkerFindingInput, 0, len(findings))
	for _, f := range findings {
		out = append(out, guidance.WorkerFindingInput{
			Path:     f.Path,
			Evidence: f.Evidence,
			Line:     f.Line,
			Excerpt:  f.Excerpt,
		})
	}
	return out
}

// toolMessageFailed trusts structured outcomes and treats empty unstamped rows as failures.
func toolMessageFailed(msg api.Message) bool {
	if tr := msg.ToolResult; tr != nil && tr.Outcome != "" {
		return tr.Outcome != api.ToolResultOutcomeCompleted
	}
	return strings.TrimSpace(msg.Content) == ""
}

// FormatFeedback preserves the occurrence's frozen copy through retry and delivery.
func (r WorkerSummaryEvalResult) FormatFeedback(ctx context.Context, hints *guidance.HintConfig) (string, error) {
	if r.HintCopy != nil {
		return guidance.RenderPolicyCopy(ctx, r.HintCode, string(r.HintEffect), r.HintCopy)
	}
	return guidance.FormatWorkerSummaryFeedback(hints, r.HintCode, r.HintData)
}

// PolicyFeedback freezes the decision and its occurrence data for durable delivery.
func (r WorkerSummaryEvalResult) PolicyFeedback() *api.WorkerPolicyFeedback {
	if r.HintCode == "" {
		return nil
	}
	return &api.WorkerPolicyFeedback{Code: r.HintCode, Effect: string(r.HintEffect), Copy: maps.Clone(r.HintCopy), Details: jsonvalue.CloneMap(r.HintData)}
}
