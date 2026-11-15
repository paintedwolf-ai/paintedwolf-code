package workerplan

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// gitDiffAdapter bridges *git.Manager to GitDiffReader.
type gitDiffAdapter struct {
	mgr *git.Manager
}

// NewGitDiffAdapter adapts mgr to GitDiffReader.
func NewGitDiffAdapter(mgr *git.Manager) GitDiffReader {
	if mgr == nil {
		return nil
	}
	return gitDiffAdapter{mgr: mgr}
}

func (g gitDiffAdapter) HasUncommittedDiff(ctx context.Context, projectDir, path string) (bool, error) {
	if g.mgr == nil || strings.TrimSpace(projectDir) == "" || strings.TrimSpace(path) == "" {
		return false, nil
	}
	diff, err := g.mgr.Diff(ctx, projectDir, git.GitDiffOpts{Paths: []string{path}})
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(diff) != "", nil
}

func (g gitDiffAdapter) DiffFromRef(ctx context.Context, projectDir, baseSHA, path string) (bool, error) {
	if g.mgr == nil || strings.TrimSpace(projectDir) == "" || strings.TrimSpace(baseSHA) == "" || strings.TrimSpace(path) == "" {
		return false, nil
	}
	return g.mgr.DiffFromRef(ctx, projectDir, baseSHA, path)
}

// DeliveryInput supplies round facts for typed criteria evaluation at delivery.
type DeliveryInput struct {
	Worker         workerrecords.Worker
	JobID          string
	ChildSessionID string
	ProjectDir     string
	JobComplete    bool
	Summary        string
	ModifiedPaths  []string
	ChangeReport   *api.WorkerChangeReport
}

// EvaluateRoundDelivery looks up jobID's worker and evaluates its typed criteria before settlement.
func (s *Service) EvaluateRoundDelivery(
	ctx context.Context,
	parentSessionID string,
	jobID string,
	childSessionID string,
	projectDir string,
	jobComplete bool,
	summary string,
	divergedPaths []string,
	report *api.WorkerChangeReport,
) []workerrecords.CriterionOutcome {
	parentSessionID = strings.TrimSpace(parentSessionID)
	if s == nil || s.Records == nil || parentSessionID == "" || strings.TrimSpace(jobID) == "" {
		return nil
	}
	round, ok, err := s.Records.RoundByJob(ctx, jobID)
	if err != nil || !ok {
		return nil
	}
	actor := workerrecords.Actor{
		SessionID:   parentSessionID,
		Role:        workerrecords.RoleCoordinator,
		PrincipalID: parentSessionID,
	}
	w, err := s.Records.Worker(ctx, actor, round.WorkerID)
	if err != nil || len(w.Criteria) == 0 {
		return nil
	}
	return s.EvaluateCriteria(ctx, DeliveryInput{
		Worker:         w,
		JobID:          jobID,
		ChildSessionID: childSessionID,
		ProjectDir:     projectDir,
		JobComplete:    jobComplete,
		Summary:        summary,
		ModifiedPaths:  divergedPaths,
		ChangeReport:   report,
	})
}

// EvaluateCriteria evaluates every typed criterion declared on in.Worker.
func (s *Service) EvaluateCriteria(ctx context.Context, in DeliveryInput) []workerrecords.CriterionOutcome {
	if len(in.Worker.Criteria) == 0 {
		return nil
	}
	allChanged := collectModifiedPaths(in.ModifiedPaths, in.ChangeReport)
	out := make([]workerrecords.CriterionOutcome, 0, len(in.Worker.Criteria))
	for _, raw := range in.Worker.Criteria {
		criterion := strings.TrimSpace(raw)
		if criterion == "" {
			continue
		}
		out = append(out, s.evaluateOne(ctx, criterion, in, allChanged))
	}
	return out
}

func (s *Service) evaluateOne(
	ctx context.Context,
	criterion string,
	in DeliveryInput,
	changedPaths []string,
) workerrecords.CriterionOutcome {
	switch {
	case criterion == "job:complete":
		if in.JobComplete {
			return workerrecords.CriterionOutcome{Criterion: criterion, Passed: true}
		}
		return workerrecords.CriterionOutcome{Criterion: criterion, Passed: false, Detail: "round did not complete"}
	case criterion == "summary:present":
		if strings.TrimSpace(in.Summary) != "" {
			return workerrecords.CriterionOutcome{Criterion: criterion, Passed: true}
		}
		return workerrecords.CriterionOutcome{Criterion: criterion, Passed: false, Detail: "completion summary is empty"}
	case strings.HasPrefix(criterion, "file:modified:"):
		target := strings.TrimSpace(strings.TrimPrefix(criterion, "file:modified:"))
		passed, detail := s.checkFileModified(ctx, target, in, changedPaths)
		return workerrecords.CriterionOutcome{Criterion: criterion, Passed: passed, Detail: detail}
	case strings.HasPrefix(criterion, "test:pass:"):
		cmd := strings.TrimSpace(strings.TrimPrefix(criterion, "test:pass:"))
		passed, detail := s.checkTestPass(ctx, cmd, in)
		return workerrecords.CriterionOutcome{Criterion: criterion, Passed: passed, Detail: detail}
	default:
		return workerrecords.CriterionOutcome{Criterion: criterion, Passed: true}
	}
}

func (s *Service) checkFileModified(
	ctx context.Context,
	target string,
	in DeliveryInput,
	changedPaths []string,
) (bool, string) {
	cleaned := cleanPaths([]string{target})
	if len(cleaned) == 0 {
		return false, "empty file target"
	}
	want := cleaned[0]
	for _, mod := range changedPaths {
		if singlePathOverlap(mod, want) {
			return true, ""
		}
	}
	if s != nil && s.GitDiff != nil && strings.TrimSpace(in.ProjectDir) != "" {
		if ok, err := s.GitDiff.HasUncommittedDiff(ctx, in.ProjectDir, want); err == nil && ok {
			return true, ""
		}
		if strings.TrimSpace(in.Worker.BaseSHA) != "" {
			if ok, err := s.GitDiff.DiffFromRef(ctx, in.ProjectDir, in.Worker.BaseSHA, want); err == nil && ok {
				return true, ""
			}
		}
	}
	return false, "file was not modified: " + want
}

func (s *Service) checkTestPass(ctx context.Context, cmd string, in DeliveryInput) (bool, string) {
	if cmd == "" {
		return false, "empty test command"
	}
	if s != nil && s.SourceRuns != nil && strings.TrimSpace(in.ChildSessionID) != "" {
		if runs, err := s.SourceRuns.WorkerSourceRuns(ctx, in.ChildSessionID); err == nil {
			for i := len(runs) - 1; i >= 0; i-- {
				if !commandsurface.SameCommandLine(runs[i].Command, cmd) {
					continue
				}
				if runs[i].Verdict == string(evidence.GateVerdictPassed) {
					return true, ""
				}
				return false, "latest run failed for " + cmd
			}
		}
	}
	if s != nil && s.Inspector != nil {
		for _, scopeID := range nonEmptyUnique(in.Worker.WorkflowRunID, in.Worker.SessionID, in.Worker.DelegationID) {
			for _, taskID := range nonEmptyUnique(in.Worker.WorkflowWorkID, in.Worker.Name, in.JobID, in.Worker.LegID) {
				status, err := s.Inspector.GetStatus(ctx, scopeID, taskID)
				if err != nil || status == nil {
					continue
				}
				rec := status.Evidence[evidence.GateTypeVerify]
				if rec == nil {
					continue
				}
				got, _ := rec.Artifacts["command"].(string)
				if commandsurface.SameCommandLine(got, cmd) && inspector.ExitCodePassed(rec.Artifacts) {
					return true, ""
				}
			}
		}
	}
	return false, "missing passing verification receipt for " + cmd
}

func collectModifiedPaths(diverged []string, report *api.WorkerChangeReport) []string {
	var raw []string
	raw = append(raw, diverged...)
	if report != nil {
		raw = append(raw, report.ChangedPaths...)
		for _, ds := range report.DiffStat {
			raw = append(raw, ds.Path)
		}
	}
	return cleanPaths(raw)
}

func nonEmptyUnique(items ...string) []string {
	var out []string
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed != "" && !seen[trimmed] {
			seen[trimmed] = true
			out = append(out, trimmed)
		}
	}
	return out
}
