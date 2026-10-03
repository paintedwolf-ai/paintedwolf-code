package workflow

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/scan"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// CoverageFacts loads the same observations used by verdict admission and reports.
func (m *RunManager) CoverageFacts(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest) (reviewcoverage.Facts, error) {
	vars, err := m.Store.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return reviewcoverage.Facts{}, err
	}
	inventory, err := LoadRunInventory(ctx, m.Inventory, run.ID)
	if err != nil {
		return reviewcoverage.Facts{}, err
	}
	if m.Inventory == nil {
		return reviewcoverage.Facts{}, fmt.Errorf("coverage scan ledger unavailable")
	}
	if !inventory.Settled {
		return reviewcoverage.Facts{}, fmt.Errorf("coverage scans have not settled")
	}
	if m.WorkerTasks == nil {
		return reviewcoverage.Facts{}, fmt.Errorf("coverage worker ledger unavailable")
	}
	tasks, err := m.WorkerTasks(ctx, run.ID)
	if err != nil {
		return reviewcoverage.Facts{}, err
	}
	return BuildCoverageFacts(manifest, vars, tasks, inventory.Scans), nil
}

// BuildCoverageFacts keeps full identities while bounding prompt path samples.
func BuildCoverageFacts(manifest workflowdef.Manifest, vars map[string]any, tasks []api.WorkerTask, scans []api.CodeScan) reviewcoverage.Facts {
	tasks = coverageReviewTasks(manifest, tasks)
	var facts reviewcoverage.Facts
	var plans []string
	for _, phase := range manifest.PhaseDefs {
		if plan, ok := FanoutPlanForPhase(vars, phase); ok {
			plans = append(plans, reviewcoverage.Identity(plan))
			for i, leg := range FanoutCoverage(plan, tasks, phase.ID) {
				facts.Obligations = append(facts.Obligations, reviewcoverage.Fact{Blocking: leg.Status != "complete" && leg.Status != "partial", ID: phase.ID + "/" + leg.ID, Kind: "planned_area", Subject: leg.Subject + " (" + leg.Status + ")", Question: plan.Legs[i].Prompt, Scope: plan.Legs[i].Scope, Tasks: leg.Attempts})
			}
		} else if workflowdef.PhaseHasGate(phase, "worker_cycle_ready") {
			facts.Obligations = append(facts.Obligations, reviewcoverage.Fact{ID: phase.ID + "/plan", Kind: "planned_area", Subject: "The planned review is unavailable", Blocking: true})
		}
		if phase.ReviewLoop != nil && phase.ReviewLoop.FollowupAttempts > 0 {
			questions, err := reviewQuestions(vars, phase.ID)
			if err != nil {
				facts.Gaps = append(facts.Gaps, reviewcoverage.Fact{ID: phase.ID + "/questions-unavailable", Kind: "review_state", Subject: err.Error(), Blocking: true})
			}
			for _, q := range questions {
				facts.Gaps = append(facts.Gaps, questionCoverageFact(q, tasks, phase))
			}
		}
		for _, obligation := range phase.OnEnter.Obligations {
			if obligation.Kind == scan.WorkflowObligationKind {
				facts.Obligations = append(facts.Obligations, reviewcoverage.Fact{ID: phase.ID + "/scans", Kind: "scan_inventory", Subject: "Assess the required scanner inventory"})
				break
			}
		}
	}
	facts.Gaps = append(facts.Gaps, scanCoverageFacts(scans)...)
	facts.Gaps = append(facts.Gaps, workerCoverageFacts(tasks)...)
	facts.Seal()
	// Evidence identities change even when the summarized state stays the same.
	ids := append([]string(nil), plans...)
	for _, s := range scans {
		ids = append(ids, reviewcoverage.Identity(struct{ ID, Snapshot, Status, Coverage string }{s.ID, s.SourceSnapshotID, string(s.Status), string(s.CoverageStatus)}))
	}
	for _, t := range tasks {
		ids = append(ids, reviewcoverage.Identity(struct {
			ID, Status string
			Result     *api.WorkerResult
		}{t.ID, string(t.Status), t.Result}))
	}
	slices.Sort(ids)
	facts.Revision = reviewcoverage.Identity(struct {
		Facts    reviewcoverage.Facts
		Evidence []string
	}{facts, ids})
	return facts
}

func scanCoverageFacts(scans []api.CodeScan) []reviewcoverage.Fact {
	var gaps []reviewcoverage.Fact
	moved := map[string][]string{}
	for _, gap := range scan.RunCoverageGaps(scans) {
		for _, path := range gap.Moved {
			moved[path] = append(moved[path], gap.ScanID)
		}
	}
	for path, ids := range moved {
		slices.Sort(ids)
		subject, paths := "Changed source", []string{path}
		if path == "" {
			subject, paths = "Changed source with unknown affected paths", nil
		}
		gaps = append(gaps, coverageGap("source_changed", subject, paths, ids, ids))
	}
	for _, s := range scan.UnrecoveredScans(scans) {
		fact := coverageGap("scan_failed", s.ScannerID, s.TargetPaths, []string{s.ID}, s.Status)
		fact.Blocking = true
		gaps = append(gaps, fact)
	}
	for _, s := range scans {
		if s.SourceCaptureQuality == "observed" || s.CoverageStatus == api.ScanCoverageBounded || (s.CoverageStatus == api.ScanCoveragePartial && len(s.Warnings) == 0) {
			gaps = append(gaps, coverageGap("source_scope", s.ScannerID, nil, []string{s.ID}, struct{ Quality, Admission, Coverage string }{s.SourceCaptureQuality, s.SourceAdmissionMode, string(s.CoverageStatus)}))
		}
		byKind := map[string][]api.ScanWarning{}
		for _, w := range s.Warnings {
			if !scan.WarningRetryable(w.Kind) {
				byKind[string(w.Kind)] = append(byKind[string(w.Kind)], w)
			}
		}
		for kind, warnings := range byKind {
			paths := make([]string, 0, len(warnings))
			for _, w := range warnings {
				if w.File != "" {
					paths = append(paths, w.File)
				}
			}
			// Warning order is an engine detail, not a new review obligation.
			slices.SortFunc(warnings, func(a, b api.ScanWarning) int {
				return cmp.Or(strings.Compare(a.File, b.File), strings.Compare(a.RuleID, b.RuleID), cmp.Compare(a.StartLine, b.StartLine), cmp.Compare(a.StartColumn, b.StartColumn), strings.Compare(a.Construct, b.Construct), strings.Compare(a.Message, b.Message))
			})
			fact := coverageGap(kind, s.ScannerID, paths, []string{s.ID}, warnings)
			fact.Count = len(warnings)
			gaps = append(gaps, fact)
		}
	}
	return gaps
}

func workerCoverageFacts(tasks []api.WorkerTask) []reviewcoverage.Fact {
	var gaps []reviewcoverage.Fact
	for _, task := range tasks {
		if task.WorkflowWorkID != "" {
			continue
		}
		if task.Status == api.WorkerStatusComplete && task.Result != nil && task.Result.CompletionReport != nil && task.Result.CompletionReport.LegStatus == "complete" {
			continue
		}
		fact := coverageGap("supporting_work", task.AgentType, nil, nil, task)
		fact.Tasks = []string{task.ID}
		gaps = append(gaps, fact)
	}
	return gaps
}

func coverageGap(kind, scanner string, paths, scans []string, evidence any) reviewcoverage.Fact {
	paths = append([]string(nil), paths...)
	slices.Sort(paths)
	paths = slices.Compact(paths)
	fact := reviewcoverage.Fact{Kind: kind, Subject: scanner, FileCount: len(paths), Paths: paths, Scans: scans, Count: len(paths)}
	fact.ID = "gap/" + reviewcoverage.Identity(struct {
		Fact     reviewcoverage.Fact
		Evidence any
	}{fact, evidence})
	if len(fact.Paths) > 12 {
		fact.Paths = fact.Paths[:12]
	}
	return fact
}

// ParseVerdictCoverage reads the single coverage_review field declared by a phase.
func ParseVerdictCoverage(def workflowdef.ReviewLoopDef, verdict map[string]string) (*reviewcoverage.Review, error) {
	var out *reviewcoverage.Review
	for field, kind := range def.VerdictSchema {
		if kind != workflowdef.VerdictCoverageType {
			continue
		}
		if out != nil {
			return nil, fmt.Errorf("verdict declares multiple coverage reviews")
		}
		dec := json.NewDecoder(strings.NewReader(verdict[field]))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&out); err != nil {
			return nil, fmt.Errorf("coverage review: %w", err)
		}
		if out == nil {
			return nil, fmt.Errorf("coverage review must be an object")
		}
		if err := dec.Decode(&struct{}{}); err != io.EOF {
			return nil, fmt.Errorf("coverage review must contain one object")
		}
	}
	return out, nil
}

// RunCoverageReview returns only the latest phase's accepted coverage review.
// A reconciling phase cannot inherit an unchallenged candidate assessment.
func RunCoverageReview(verdicts []PhaseVerdict) *reviewcoverage.Review {
	var out *reviewcoverage.Review
	for _, v := range verdicts {
		if !v.Def.CarriesCoverage() {
			continue
		}
		out = nil
		if v.Record.GateVerdict != "approved" {
			continue
		}
		review, err := ParseVerdictCoverage(v.Def, VerdictMembers(v.Record.Artifacts))
		if err == nil {
			out = review
		}
	}
	return out
}

func (m *RunManager) checkReviewCoverage(ctx context.Context, run *api.WorkflowRun, def workflowdef.ReviewLoopDef, verdict map[string]string) error {
	review, err := ParseVerdictCoverage(def, verdict)
	if err != nil || review == nil {
		return err
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return err
	}
	facts, err := m.CoverageFacts(ctx, run, manifest)
	if err != nil {
		return err
	}
	return reviewcoverage.Validate(facts, *review)
}

// Coverage ends at the final assessment phase; report and follow-on work do not
// rewrite the evidence the assessment was made against.
func coverageReviewTasks(manifest workflowdef.Manifest, tasks []api.WorkerTask) []api.WorkerTask {
	last := -1
	for i, phase := range manifest.PhaseDefs {
		if phase.ReviewLoop != nil && phase.ReviewLoop.CarriesCoverage() {
			last = i
		}
	}
	if last < 0 {
		return tasks
	}
	later := map[string]bool{}
	for _, phase := range manifest.PhaseDefs[last+1:] {
		later[phase.ID] = true
	}
	return slices.DeleteFunc(slices.Clone(tasks), func(task api.WorkerTask) bool { return later[task.WorkflowPhase] })
}
