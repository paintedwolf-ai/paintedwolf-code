package workflowadmin

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/report"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// runAccount is the host's account of a run's work: the recorded facts behind
// the report, the gaps they leave, and what the brief says was checked.
type runAccount struct {
	coverage  []report.ReportCoverageItem
	gaps      []report.ReportGap
	checks    []report.ReportCheck
	inventory *report.ReportInventory
}

func (a *runAccount) gap(g report.ReportGap) {
	if g.Count > 0 {
		a.gaps = append(a.gaps, g)
	}
}

// scanAccount records the run's bound scans, separating gaps a rescan would
// close from standing scanner limits, and accounts for every scanner group:
// linked by a claim or finding, or set aside by a review phase or the closeout.
func (a *runAccount) scanAccount(scans []wire.CodeScan, completion *wire.CompletionReportMeta, claims []workflow.RunClaim, reviewed []scanfindings.SetAside) {
	if len(scans) == 0 {
		return
	}
	observed := summarizeScanCoverage(scans)
	a.coverage = append(a.coverage, observed.coverage...)
	for _, gap := range observed.gaps {
		a.gap(gap)
	}

	sets := append(append([]scanfindings.SetAside(nil), reviewed...), setAsides(completion)...)
	account := scanfindings.AccountInventory(scanfindings.InventoryGroups(scanfindings.InventoryFindings(scans)),
		linkedGroups(completion, claims), sets)
	inv := &report.ReportInventory{
		Total:       account.Total(),
		Linked:      account.LinkedCount(),
		SetAside:    account.SetAsideCount(),
		Unaccounted: len(account.Unaccounted()),
	}
	counts := make([]int, len(sets))
	for id, index := range account.SetAsideBy {
		if !account.Linked[id] {
			counts[index]++
		}
	}
	for i, sa := range sets {
		if counts[i] > 0 {
			inv.SetAsides = append(inv.SetAsides, report.ReportSetAside{Reason: sa.Reason, Groups: counts[i]})
		}
	}
	a.inventory = inv
	a.gap(report.ReportGap{Kind: report.GapInventoryUnaccounted, Count: inv.Unaccounted, Of: inv.Total})
	a.coverage = append(a.coverage, report.ReportCoverageItem{
		Subject: "Scanner inventory", Status: "Accounting",
		Detail: fmt.Sprintf("%d of %d groups assessed by a claim or finding; %d set aside; %d unaccounted",
			inv.Linked, inv.Total, inv.SetAside, inv.Unaccounted),
	})

	check := report.ReportCheck{
		Kind: report.CheckScans, Ran: observed.scanners, ScansFailed: observed.failed,
		Used: inv.Linked + inv.SetAside, Total: inv.Total,
	}
	switch {
	case check.Total == 0 || check.Used >= check.Total:
		check.State = report.CheckDone
	case check.Used == 0:
		check.State = report.CheckUnchecked
	default:
		check.State = report.CheckPartial
	}
	a.checks = append(a.checks, check)
}

// Path-targeted scans report their declared scope.
func scanCoverageDetail(s wire.CodeScan) string {
	if s.TargetKind == wire.ScanTargetPaths {
		n := len(s.TargetPaths)
		return fmt.Sprintf("Targeted scan %s of %d %s; %d stored findings", s.ID, n, plural(n, "file", "files"), len(s.Findings))
	}
	return fmt.Sprintf("Scan %s; source snapshot %s; coverage %s; %d stored findings", s.ID, s.SourceSnapshotID, s.CoverageStatus, len(s.Findings))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func linkedGroups(completion *wire.CompletionReportMeta, claims []workflow.RunClaim) []string {
	var out []string
	for _, c := range claims {
		out = append(out, c.ScanGroupIDs...)
	}
	if completion != nil {
		for _, f := range completion.Findings {
			out = append(out, f.ScanGroupIds...)
		}
	}
	return out
}

func setAsides(completion *wire.CompletionReportMeta) []scanfindings.SetAside {
	if completion == nil {
		return nil
	}
	out := make([]scanfindings.SetAside, 0, len(completion.SetAsides))
	for _, sa := range completion.SetAsides {
		out = append(out, scanfindings.SetAside{GroupIDs: sa.ScanGroupIds, Scanner: sa.Scanner, Paths: sa.Paths, Reason: sa.Reason})
	}
	return out
}

// claimAccount states where the run's claims stand: open claims are unfinished
// work, and each review phase a workflow names for a reader is a check.
func (a *runAccount) claimAccount(manifest workflowdef.Manifest, claims []workflow.RunClaim) {
	open := report.ReportGap{Kind: report.GapClaimsOpen, Of: len(claims)}
	for _, c := range claims {
		if c.Class == workflowdef.ClaimOpen {
			open.Count++
			open.Names = append(open.Names, claimName(c))
		}
	}
	a.gap(open)
	for _, def := range manifest.PhaseDefs {
		rl := def.ReviewLoop
		if rl == nil || rl.BriefLabel == "" {
			continue
		}
		check := report.ReportCheck{Kind: report.CheckReview, Subject: rl.BriefLabel}
		for _, c := range claims {
			stated := c.Phase == def.ID || (c.Dropped && c.Phase == rl.ReconcilesPhase)
			if !stated {
				continue
			}
			switch c.Class {
			case workflowdef.ClaimHeld:
				check.Held++
			case workflowdef.ClaimFailed:
				check.Failed++
			default:
				check.Open++
			}
		}
		if check.Held+check.Failed+check.Open == 0 {
			continue
		}
		check.State = report.CheckDone
		if check.Open > 0 {
			check.State = report.CheckPartial
		}
		a.checks = append(a.checks, check)
	}
}

func claimName(c workflow.RunClaim) string {
	if t := strings.TrimSpace(c.Title); t != "" {
		return t
	}
	return c.ID
}

// workAccount accounts for the run's worker legs: every planned leg is a
// check, and legs and helpers that stopped short are gaps.
func (s *Handler) workAccount(ctx context.Context, a *runAccount, run *wire.WorkflowRun, manifest workflowdef.Manifest) error {
	if s.Workers == nil {
		a.coverage = append(a.coverage, report.ReportCoverageItem{Subject: "Workers", Status: "Unavailable", Detail: "Worker accounting is unavailable."})
		return nil
	}
	vars, err := s.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return err
	}
	tasks, err := s.Workers.ListByWorkflowRunID(ctx, run.ID)
	if err != nil {
		return err
	}
	a.accountWorkers(manifest, vars, tasks)
	return nil
}

func (a *runAccount) accountWorkers(manifest workflowdef.Manifest, vars map[string]any, tasks []wire.WorkerTask) {
	taskByID := make(map[string]wire.WorkerTask, len(tasks))
	for _, task := range tasks {
		taskByID[task.ID] = task
	}
	unfinished := report.ReportGap{Kind: report.GapLegsUnfinished}
	partial := report.ReportGap{Kind: report.GapLegsPartial}
	legAttempts := map[string]bool{}
	for _, phase := range manifest.PhaseDefs {
		plan, ok := workflow.FanoutPlanForPhase(vars, phase)
		if !ok {
			continue
		}
		for _, leg := range workflow.FanoutCoverage(plan, tasks, phase.ID) {
			for _, id := range leg.Attempts {
				legAttempts[id] = true
			}
			detail := fmt.Sprintf("%s · %d of %d allowed attempts: %s", leg.Subject, len(leg.Attempts), plan.MaxAttempts, strings.Join(leg.Attempts, ", "))
			if len(leg.Attempts) > 0 {
				lastTask := taskByID[leg.Attempts[len(leg.Attempts)-1]]
				if lastTask.Result != nil && strings.TrimSpace(lastTask.Result.HintCode) != "" {
					detail += " · host check: " + guidance.HintCodeUILabel(lastTask.Result.HintCode)
				}
			}
			a.coverage = append(a.coverage, report.ReportCoverageItem{
				Subject: phase.ID + "/" + leg.ID + " · " + leg.AgentType,
				Status:  leg.Status,
				Detail:  detail,
			})
			check := report.ReportCheck{Kind: report.CheckArea, Subject: leg.Subject}
			unfinished.Of++
			partial.Of++
			switch leg.Status {
			case "complete":
				check.State = report.CheckDone
			case "partial":
				check.State = report.CheckPartial
				partial.Count++
				partial.Names = append(partial.Names, leg.Subject)
			default:
				check.State = report.CheckUnchecked
				unfinished.Count++
				unfinished.Names = append(unfinished.Names, leg.Subject)
			}
			a.checks = append(a.checks, check)
		}
	}
	a.gap(unfinished)
	a.gap(partial)

	helpers := report.ReportGap{Kind: report.GapWorkersPartial}
	for _, task := range tasks {
		// A leg's attempts are already listed under their leg.
		if legAttempts[task.ID] {
			continue
		}
		status := wire.WorkerTaskLegStatus(task)
		detail := "Attempt " + task.ID
		if task.Result != nil && strings.TrimSpace(task.Result.HintCode) != "" {
			detail += " · host check: " + guidance.HintCodeUILabel(task.Result.HintCode)
		}
		a.coverage = append(a.coverage, report.ReportCoverageItem{Subject: task.AgentType + " · " + task.WorkflowPhase, Status: status, Detail: detail})
		if task.WorkflowWorkID != "" {
			continue
		}
		helpers.Of++
		complete := wire.WorkerTaskLegStatus(task) == "complete"
		if !complete {
			helpers.Count++
			helpers.Names = append(helpers.Names, task.AgentType)
		}
	}
	a.gap(helpers)
}
