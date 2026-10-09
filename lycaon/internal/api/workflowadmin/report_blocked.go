package workflowadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/report"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// blockedRunReport renders retained work without manufacturing a completion.
func (s *Handler) blockedRunReport(ctx context.Context, run *wire.WorkflowRun, manifest workflowdef.Manifest) (report.ReportInput, bool, error) {
	vars, err := s.Runs.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return report.ReportInput{}, false, err
	}
	repair, err := runstate.CurrentReviewRepair(vars, run.CurrentPhase)
	if err != nil || repair == nil || repair.State != "blocked" || repair.Snapshot == nil {
		return report.ReportInput{}, false, err
	}
	input := report.ReportInput{
		ReportHeader: report.ReportHeader{
			Kind:        report.BlockedReviewSnapshot,
			Title:       manifest.Name,
			Headline:    "Review incomplete",
			RunID:       run.ID,
			Project:     s.reportProjectLabel(ctx, run.ProjectID),
			StartedAt:   reportStartedAt(run),
			CompletedAt: repair.UpdatedAt.Format(time.RFC3339),
			Workflow:    &report.ReportWorkflow{ID: run.WorkflowID, Version: run.WorkflowVersion},
			Summary:     fmt.Sprintf("Review paused in %s after %d rejected response attempts. No terminal verdict was recorded for this phase.", repair.Phase, len(repair.Responses)),
		},
		Ask: &report.ReportAsk{Do: "Resume the review after addressing the blocker, or cancel and retain this snapshot.", Effort: "Completed work is retained."},
	}
	var narrative strings.Builder
	narrative.WriteString(input.Summary + "\n\nBlocking diagnostics:\n")
	for _, diagnostic := range repair.Diagnostics {
		fmt.Fprintf(&narrative, "- %s\n", diagnostic.Code)
		if reason, ok := diagnostic.Details["reason"].(string); ok {
			narrative.WriteString(reason + "\n")
		}
	}
	snapshot := repair.Snapshot
	phases := snapshot.Verdicts
	claims := workflowpresentation.ReconcileClaims(phases)
	input.Claims = reportClaims(claims)
	input.Verdicts, _, _ = projectVerdicts(phases)
	for _, claim := range claims {
		finding := report.ReportFinding{ID: claim.ID, Title: claim.Title, Status: claim.Status, Impact: claim.Statement, Disposition: report.DispositionUnresolved}
		for _, cite := range claim.CitedEvidence {
			finding.Where = append(finding.Where, report.ReportClaimCitation{Handle: cite.Handle, Path: cite.Path, Line: cite.Line})
		}
		input.Findings = append(input.Findings, finding)
	}
	if len(snapshot.Candidate) > 0 {
		raw, err := json.MarshalIndent(snapshot.Candidate, "", "  ")
		if err != nil {
			return input, false, fmt.Errorf("encode retained review candidate: %w", err)
		}
		fmt.Fprintf(&narrative, "\nUnaccepted candidate submission (not adjudicated):\n\n```json\n%s\n```\n", raw)
	}
	for _, source := range snapshot.Unavailable {
		fmt.Fprintf(&narrative, "\nUnavailable when paused: %s.\n", source)
	}
	narrative.WriteString("\nIndependent challenge is established only by the accepted review records listed in this report. Unaccepted candidates are not adjudicated findings.\n")
	for _, task := range snapshot.Workers {
		fmt.Fprintf(&narrative, "\nWorker %s (%s): %s\n", task.ID, task.AgentType, wire.WorkerTaskLegStatus(task))
		if task.Result == nil || task.Result.CompletionReport == nil {
			continue
		}
		worker := task.Result.CompletionReport
		narrative.WriteString(worker.Brief + "\n")
		for _, objective := range worker.ObjectivesMet {
			fmt.Fprintf(&narrative, "Reported completed scope: %s\n", objective)
		}
		for _, risk := range worker.RemainingRisk {
			fmt.Fprintf(&narrative, "Worker-reported remaining risk: %s\n", risk)
		}
		for _, finding := range worker.Findings {
			fmt.Fprintf(&narrative, "Worker observation, not adjudicated: %s — %s:%d (%s)\n", strings.TrimSpace(finding.Claim+" "+finding.Note), finding.Path, finding.Line, finding.Evidence)
		}
		for _, gap := range worker.CoverageGaps {
			fmt.Fprintf(&narrative, "Unresolved worker scope %s: %s — %s\n", gap.ID, gap.Subject, gap.Reason)
			if len(gap.Paths) > 0 {
				fmt.Fprintf(&narrative, "Affected paths: %s\n", strings.Join(gap.Paths, ", "))
			}
		}
	}
	input.Synthesis = narrative.String()
	var account runAccount
	account.accountWorkers(manifest, snapshot.Vars, snapshot.Workers)
	scans := snapshot.Scans
	account.claimAccount(manifest, claims)
	account.scanAccount(scans, nil, claims, workflowpresentation.RunSetAsides(phases))
	for _, phase := range manifest.PhaseDefs {
		if phase.ReviewLoop == nil || len(phase.ReviewLoop.RequiredAgents) == 0 {
			continue
		}
		status := "No terminal verdict recorded"
		for _, verdict := range phases {
			if verdict.Phase == phase.ID && workflowvalidation.ReviewLoopVerdictTerminal(verdict.Def, workflowpresentation.VerdictMembers(verdict.Record.Artifacts)) {
				status = "Terminal verdict recorded"
			}
		}
		account.coverage = append(account.coverage, report.ReportCoverageItem{Subject: phase.ActivityLabel, Status: status, Detail: "Required reviewers: " + strings.Join(phase.ReviewLoop.RequiredAgents, ", ")})
	}
	input.Coverage, input.Gaps, input.Checks, input.Inventory = account.coverage, account.gaps, account.checks, account.inventory
	input.ScanRows, input.ScanRules, input.Scan, input.HeadSHA = summarizeScans(scans)
	if repair.CoverageFacts != nil {
		input.CoverageFacts = repair.CoverageFacts
	}
	return input, true, nil
}
