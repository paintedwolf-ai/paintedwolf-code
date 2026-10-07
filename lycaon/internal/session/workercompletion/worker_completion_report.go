package workercompletion

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/jsonfence"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/tools"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
	"github.com/lycaon/lycaon/internal/verification"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	WorkerCompletionReportMissingCode = "WORKER_COMPLETION_REPORT_MISSING"
	maxReportListItems                = 12
	maxReportBriefChars               = 1200
	maxReportListItemChars            = 200
	maxFindingExcerptChars            = 320
	maxFindingNoteChars               = 200
	maxSuggestedNextTaskChars         = 200
)

// WorkerCompletionReport is structured worker finish metadata.
// The host supplies file and evidence fields.
type WorkerCompletionReport struct {
	CoverageReview      *api.CoverageReview      `json:"coverage_review,omitempty"`
	Verification        *verification.Assessment `json:"verification,omitempty"`
	DeclaredLegStatus   string                   `json:"declared_leg_status,omitempty"`
	LegStatus           string                   `json:"leg_status"`
	FilesModified       []string                 `json:"files_modified,omitempty"`
	ObjectivesMet       []string                 `json:"objectives_met,omitempty"`
	RemainingRisk       []string                 `json:"remaining_risk,omitempty"`
	SuggestedNextTask   string                   `json:"suggested_next_task,omitempty"`
	Brief               string                   `json:"brief,omitempty"`
	Findings            []WorkerFinding          `json:"findings,omitempty"`
	CitedURLs           []string                 `json:"cited_urls,omitempty"`
	EvidenceObligations []EvidenceObligation     `json:"evidence_obligations,omitempty"`
}

const (
	EvidenceStatusSatisfied = "satisfied"
	EvidenceStatusUnmet     = "unmet"
)

// EvidenceObligation reports validation coverage without controlling work completion.
type EvidenceObligation struct {
	Kind         string   `json:"kind"`
	Status       string   `json:"status"`
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
	Reason       string   `json:"reason,omitempty"`
}

const (
	FindingClaimVulnerability    = "vulnerability"
	FindingClaimHardening        = "hardening"
	FindingClaimAcceptedResidual = "accepted_residual"
	FindingClaimModel            = "model"
)

const (
	FindingSeverityHigh   = "high"
	FindingSeverityMedium = "medium"
	FindingSeverityLow    = "low"
)

// WorkerFinding is one path-keyed citation in the completion report.
type WorkerFinding struct {
	Path         string `json:"path"`
	Evidence     string `json:"evidence,omitempty"` // host-minted handle hint
	Line         int    `json:"line,omitempty"`
	Excerpt      string `json:"excerpt,omitempty"`
	Note         string `json:"note,omitempty"`
	Claim        string `json:"claim,omitempty"`
	Adversary    string `json:"adversary,omitempty"`
	Precondition string `json:"precondition,omitempty"`
	Severity     string `json:"severity,omitempty"`
}

// LastCompleteLegReport decodes the latest accepted report arguments.
func LastCompleteLegReport(msgs []api.Message) (WorkerCompletionReport, string, bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		tr := msgs[i].ToolResult
		if msgs[i].Role != api.MessageRoleTool || tr == nil {
			continue
		}
		if strings.TrimSpace(tr.Tool) != workertools.CompleteLegTool {
			continue
		}
		if tr.Outcome != api.ToolResultOutcomeCompleted {
			continue
		}
		if report, ok := ReportFromCompleteLegArgs(tr.ToolArgs); ok {
			return report, msgs[i].ID, true
		}
	}
	return WorkerCompletionReport{}, "", false
}

// ReportFromCompleteLegArgs decodes a complete_leg argument snapshot.
func ReportFromCompleteLegArgs(args map[string]any) (WorkerCompletionReport, bool) {
	if len(args) == 0 {
		return WorkerCompletionReport{}, false
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return WorkerCompletionReport{}, false
	}
	return decodeWorkerCompletionReport(string(raw))
}

// CompleteLegDecoder validates complete_leg arguments.
func CompleteLegDecoder(_ context.Context, args map[string]any, _ tools.ToolContext) (workertools.CompleteLegRecord, error) {
	report, ok := ReportFromCompleteLegArgs(args)
	if !ok {
		return workertools.CompleteLegRecord{}, &tools.ToolReject{Code: "COMPLETE_LEG_STATUS_REQUIRED", Data: map[string]any{"tool": workertools.CompleteLegTool}}
	}
	return workertools.CompleteLegRecord{LegStatus: report.LegStatus, Findings: len(report.Findings)}, nil
}

// ParseWorkerCompletionReport extracts envelope-only completion JSON from assistant content.
func ParseWorkerCompletionReport(content string) (WorkerCompletionReport, bool) {
	return jsonfence.ParseStrict(content, decodeWorkerCompletionReport)
}

func decodeWorkerCompletionReport(candidate string) (WorkerCompletionReport, bool) {
	var report WorkerCompletionReport
	if err := json.Unmarshal([]byte(candidate), &report); err != nil {
		return WorkerCompletionReport{}, false
	}
	report.FilesModified = nil
	report.EvidenceObligations = nil
	report.Normalize()
	if report.LegStatus == "" {
		return WorkerCompletionReport{}, false
	}
	return report, true
}

func (r WorkerCompletionReport) Empty() bool {
	return strings.TrimSpace(r.LegStatus) == "" &&
		strings.TrimSpace(r.DeclaredLegStatus) == "" &&
		len(r.FilesModified) == 0 &&
		len(r.ObjectivesMet) == 0 &&
		len(r.RemainingRisk) == 0 &&
		strings.TrimSpace(r.SuggestedNextTask) == "" &&
		strings.TrimSpace(r.Brief) == "" &&
		len(r.Findings) == 0 &&
		len(r.CitedURLs) == 0 &&
		len(r.EvidenceObligations) == 0
}

func (r *WorkerCompletionReport) Normalize() {
	r.normalizeListsAndLeg()
	r.Brief = strings.TrimSpace(r.Brief)
	r.SuggestedNextTask = strings.TrimSpace(r.SuggestedNextTask)
}

func (r *WorkerCompletionReport) normalizeListsAndLeg() {
	r.LegStatus = NormalizeReportLegStatus(r.LegStatus)
	r.DeclaredLegStatus = NormalizeReportLegStatus(r.DeclaredLegStatus)
	r.FilesModified = normalizeReportStringList(r.FilesModified, 0)
	r.ObjectivesMet = normalizeReportStringList(r.ObjectivesMet, 0)
	r.RemainingRisk = normalizeReportStringList(r.RemainingRisk, 0)
	r.Findings = normalizeFindings(r.Findings, 0)
	r.CitedURLs = normalizeReportStringList(r.CitedURLs, 0)
	r.EvidenceObligations = normalizeEvidenceObligations(r.EvidenceObligations)
	r.SuggestedNextTask = strings.TrimSpace(r.SuggestedNextTask)
}

func normalizeEvidenceObligations(in []EvidenceObligation) []EvidenceObligation {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	var out []EvidenceObligation
	for _, obligation := range in {
		obligation.Kind = strings.TrimSpace(obligation.Kind)
		obligation.Status = strings.TrimSpace(obligation.Status)
		obligation.Reason = strings.TrimSpace(obligation.Reason)
		obligation.EvidenceRefs = normalizeReportStringList(obligation.EvidenceRefs, 0)
		if obligation.Kind == "" || obligation.Status == "" {
			continue
		}
		if _, ok := seen[obligation.Kind]; ok {
			continue
		}
		seen[obligation.Kind] = struct{}{}
		out = append(out, obligation)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

func normalizeFindings(in []WorkerFinding, max int) []WorkerFinding {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	var out []WorkerFinding
	for _, item := range in {
		item.Path = strings.TrimSpace(item.Path)
		item.Evidence = strings.TrimSpace(item.Evidence)
		item.Excerpt = strings.TrimSpace(item.Excerpt)
		item.Note = strings.TrimSpace(item.Note)
		item.Claim = normalizeFindingClaim(item.Claim)
		item.Adversary = strings.TrimSpace(item.Adversary)
		item.Precondition = strings.TrimSpace(item.Precondition)
		if item.Claim == FindingClaimVulnerability {
			item.Severity = normalizeFindingSeverity(item.Severity)
		} else {
			item.Severity = "" // only vulnerability claims carry a band
		}
		if item.Path == "" && item.Note == "" {
			continue
		}
		key := item.Path + "|" + item.Evidence + "|" + strconv.Itoa(item.Line) + "|" + item.Excerpt + "|" + item.Claim
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

func normalizeFindingClaim(claim string) string {
	switch strings.TrimSpace(strings.ToLower(claim)) {
	case FindingClaimVulnerability, FindingClaimHardening, FindingClaimAcceptedResidual, FindingClaimModel:
		return strings.TrimSpace(strings.ToLower(claim))
	default:
		return ""
	}
}

func normalizeFindingSeverity(severity string) string {
	switch strings.TrimSpace(strings.ToLower(severity)) {
	case FindingSeverityHigh, FindingSeverityMedium, FindingSeverityLow:
		return strings.TrimSpace(strings.ToLower(severity))
	default:
		return ""
	}
}

// NormalizeWorkerCompletionState validates a worker state.
func NormalizeWorkerCompletionState(state string) string {
	switch strings.TrimSpace(strings.ToLower(state)) {
	case "complete":
		return "complete"
	case "partial":
		return "partial"
	case "open":
		return "open"
	case "needs_decision":
		return "needs_decision"
	case "held":
		return "held"
	case "canceled":
		return "canceled"
	case "failed":
		return "failed"
	default:
		return ""
	}
}

func workerStateLegStatus(state string) string {
	switch NormalizeWorkerCompletionState(state) {
	case string(api.WorkerSummaryStatusComplete), string(api.WorkerSummaryStatusOpen):
		return LegStatusComplete
	case string(api.WorkerSummaryStatusNeedsDecision), string(api.WorkerSummaryStatusHeld):
		return LegStatusBlocked
	case string(api.WorkerSummaryStatusPartial), string(api.WorkerSummaryStatusCanceled), string(api.WorkerSummaryStatusFailed):
		return LegStatusPartial
	default:
		return ""
	}
}

// NormalizeReportLegStatus accepts canonical leg statuses.
func NormalizeReportLegStatus(status string) string {
	candidate := strings.TrimSpace(strings.ToLower(status))
	for _, known := range legStatuses {
		if candidate == known {
			return known
		}
	}
	return ""
}

func normalizeReportStringList(in []string, max int) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
		if max > 0 && len(out) >= max {
			break
		}
	}
	sort.Strings(out)
	return out
}

// EnrichWorkerCompletionReport sets FilesModified and evidence from host proof.
func EnrichWorkerCompletionReport(report WorkerCompletionReport, proof WorkerCompletionProof, hostStatus string) WorkerCompletionReport {
	report.FilesModified = mergeChangedPaths(proof.ChangedPaths)
	report.EvidenceObligations = compileEvidenceObligations(proof)
	if report.LegStatus == "" {
		report.LegStatus = workerStateLegStatus(hostStatus)
	}
	report.Normalize()
	return report
}

func compileEvidenceObligations(proof WorkerCompletionProof) []EvidenceObligation {
	if len(proof.ChangedPaths) == 0 {
		return nil
	}
	obligation := EvidenceObligation{
		Kind: "source_validation", Status: EvidenceStatusUnmet,
		Reason: "Changed source has no current validation evidence.",
	}
	assessment := proof.Verification
	if assessment.Valid() && assessment.Method == verification.Blocked {
		obligation.Reason = assessment.Reason
		return []EvidenceObligation{obligation}
	}
	failed := false
	for _, receipt := range currentValidationReceipts(proof) {
		if !validationReceipt(receipt, proof) {
			continue
		}
		if proof.SourceRevision == "" || proof.SourceRootDigest == "" ||
			receipt.SourceRevision != proof.SourceRevision || receipt.SourceRootDigest != proof.SourceRootDigest {
			continue
		}
		if receipt.Tool != "complete_leg" && receipt.Verdict != "passed" {
			failed = true
			continue
		}
		obligation.Status = EvidenceStatusSatisfied
		obligation.Reason = ""
		obligation.EvidenceRefs = append(obligation.EvidenceRefs, receipt.ID)
		if strings.TrimSpace(receipt.EvidenceRef) != "" {
			obligation.EvidenceRefs = append(obligation.EvidenceRefs, receipt.EvidenceRef)
		}
	}
	if failed {
		obligation.Status = EvidenceStatusUnmet
		obligation.Reason = "A current check did not pass."
	}
	return []EvidenceObligation{obligation}
}

func sourceRunReceipt(receipt WorkerInvocationReceipt) bool {
	if receipt.Status != api.InvocationStatusCompleted {
		return false
	}
	switch receipt.Tool {
	case "verify", "command":
		return true
	default:
		return false
	}
}

func validationReceipt(receipt WorkerInvocationReceipt, proof WorkerCompletionProof) bool {
	assessment := proof.Verification
	if assessment.Valid() && assessment.Method == verification.Inspection {
		return receipt.Status == api.InvocationStatusCompleted && receipt.Tool == "complete_leg"
	}
	if !sourceRunReceipt(receipt) || receipt.Verdict == "" {
		return false
	}
	if assessment.Valid() && assessment.Method == verification.Targeted {
		return receipt.IsCheck
	}
	if proof.DeclaredCommand == nil {
		return receipt.Tool == "verify"
	}
	declared := strings.TrimSpace(*proof.DeclaredCommand)
	if declared == "" {
		return receipt.Tool == "verify" || receipt.IsCheck
	}
	return commandsurface.SameCommandLine(receipt.Command, declared) && receipt.Cwd == "."
}

// SourceEvidenceView returns the current source-validation obligation.
func SourceEvidenceView(proof WorkerCompletionProof) (status, reason string, refs []string) {
	obligations := compileEvidenceObligations(proof)
	if len(obligations) == 0 {
		return EvidenceStatusSatisfied, "", nil
	}
	return obligations[0].Status, obligations[0].Reason, append([]string(nil), obligations[0].EvidenceRefs...)
}

// SynthesizeCompletionReport builds a bounded report from child tool receipts.
func SynthesizeCompletionReport(msgs []api.Message, agentType string, proof WorkerCompletionProof) (WorkerCompletionReport, bool) {
	excerpts := collectToolExcerpts(msgs, 1200)
	if excerpts == "" && proof.ReceiptCount == 0 && len(proof.ChangedPaths) == 0 {
		return WorkerCompletionReport{}, false
	}
	report := WorkerCompletionReport{
		LegStatus:     LegStatusPartial,
		FilesModified: append([]string(nil), proof.ChangedPaths...),
		ObjectivesMet: synthesizeObjectivesFromProof(proof, excerpts),
		RemainingRisk: []string{"Worker did not call complete_leg"},
		Brief:         synthesizeBrief(agentType, proof),
	}
	report.Normalize()
	return report, true
}

func synthesizeObjectivesFromProof(proof WorkerCompletionProof, excerpts string) []string {
	var out []string
	if proof.ReceiptCount > 0 {
		out = append(out, "Survey receipts: "+strconv.Itoa(proof.ReceiptCount))
	}
	if len(proof.MutationTools) > 0 {
		out = append(out, "Mutation tools: "+strings.Join(proof.MutationTools, ", "))
	}
	if excerpts != "" {
		lines := strings.Split(excerpts, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "###") || strings.HasPrefix(line, "---") {
				continue
			}
			line = runeclamp.Clamp(line, 160)
			out = append(out, line)
			if len(out) >= 5 {
				break
			}
		}
	}
	return normalizeReportStringList(out, 5)
}

// AnnotateWorkerReportForBranch adds merge routing hints for isolated write workers.
func AnnotateWorkerReportForBranch(report WorkerCompletionReport, task *api.WorkerTask) WorkerCompletionReport {
	if task == nil || !task.EffectiveScope().IsWrite() || strings.TrimSpace(task.WorkspaceRoot) == "" {
		return report
	}
	if strings.TrimSpace(report.SuggestedNextTask) == "" {
		report.SuggestedNextTask = "preview_overlay then promote_overlay for overlay " + strings.TrimSpace(task.ID)
	}
	return report
}

func synthesizeBrief(agentType string, proof WorkerCompletionProof) string {
	agent := strings.TrimSpace(agentType)
	if agent == "" {
		agent = "worker"
	}
	if proof.ReceiptCount > 0 {
		return agent + " finished survey without an accepted completion report; host compiled tool receipts."
	}
	if len(proof.ChangedPaths) > 0 {
		return agent + " changed " + strings.Join(proof.ChangedPaths, ", ") + " without completion JSON."
	}
	return agent + " finished without structured completion JSON; host compiled tool excerpts."
}

func mergeChangedPaths(groups ...[]string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, group := range groups {
		for _, p := range group {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// ParentReport bounds presentation after the original report has been evaluated and retained.
func (r WorkerCompletionReport) ParentReport() WorkerCompletionReport {
	r.Normalize()
	r.Brief = runeclamp.Fit(r.Brief, maxReportBriefChars)
	r.SuggestedNextTask = runeclamp.Fit(r.SuggestedNextTask, maxSuggestedNextTaskChars)
	r.FilesModified = normalizeReportStringList(r.FilesModified, maxReportListItems)
	r.ObjectivesMet = parentReportNarrativeList(r.ObjectivesMet)
	r.RemainingRisk = parentReportNarrativeList(r.RemainingRisk)
	r.CitedURLs = normalizeReportStringList(r.CitedURLs, maxReportListItems)
	r.Findings = normalizeFindings(r.Findings, maxReportListItems)
	for i := range r.Findings {
		r.Findings[i].Note = runeclamp.Fit(r.Findings[i].Note, maxFindingNoteChars)
		// A truncated quotation is no longer the observed excerpt. The path and line remain addressable.
		if len([]rune(r.Findings[i].Excerpt)) > maxFindingExcerptChars {
			r.Findings[i].Excerpt = ""
		}
	}
	return r
}

func parentReportNarrativeList(items []string) []string {
	result := normalizeReportStringList(items, maxReportListItems)
	for i := range result {
		result[i] = runeclamp.Fit(result[i], maxReportListItemChars)
	}
	return result
}
