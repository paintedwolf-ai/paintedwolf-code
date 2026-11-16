package workerplan

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// Typed criteria a planned worker's result is checked against.
const (
	criterionJobComplete    = "job:complete"
	criterionSummaryPresent = "summary:present"
	criterionFileModified   = "file:modified:"
	criterionTestPass       = "test:pass:"
)

// Why a criterion did not pass.
const (
	detailRoundIncomplete = "round_incomplete"
	detailSummaryEmpty    = "summary_empty"
	detailFileUnmodified  = "file_unmodified"
	detailRunFailed       = "run_failed"
	detailRunMissing      = "run_missing"
	detailTargetEmpty     = "target_empty"
)

type gitDiffAdapter struct {
	mgr *git.Manager
}

// NewGitDiffAdapter adapts mgr to GitDiffReader. A folder outside Git has no
// diff: the round's own change report decides file criteria there.
func NewGitDiffAdapter(mgr *git.Manager) GitDiffReader {
	return gitDiffAdapter{mgr: mgr}
}

func (g gitDiffAdapter) HasUncommittedDiff(ctx context.Context, projectDir, path string) (bool, error) {
	diff, err := g.mgr.Diff(ctx, projectDir, git.GitDiffOpts{Paths: []string{path}})
	if errors.Is(err, git.ErrNotRepository) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(diff) != "", nil
}

func (g gitDiffAdapter) DiffFromRef(ctx context.Context, projectDir, baseSHA, path string) (bool, error) {
	modified, err := g.mgr.DiffFromRef(ctx, projectDir, baseSHA, path)
	if errors.Is(err, git.ErrNotRepository) {
		return false, nil
	}
	return modified, err
}

// DeliveryInput is what a round delivered, for criteria evaluation.
type DeliveryInput struct {
	Worker         workerrecords.Worker
	ChildSessionID string
	ProjectDir     string
	JobComplete    bool
	Summary        string
	ModifiedPaths  []string
	ChangeReport   *api.WorkerChangeReport
}

// EvaluateRoundDelivery evaluates the typed criteria of the worker that ran
// jobID. It returns nil when the job belongs to no worker with criteria.
func (s *Service) EvaluateRoundDelivery(ctx context.Context, parentSessionID, jobID string, in DeliveryInput) ([]workerrecords.CriterionOutcome, error) {
	round, ok, err := s.records.RoundByJob(ctx, jobID)
	if err != nil || !ok {
		return nil, err
	}
	in.Worker, err = s.records.Worker(ctx, coordinator(parentSessionID), round.WorkerID)
	if err != nil {
		return nil, err
	}
	return s.EvaluateCriteria(ctx, in)
}

// EvaluateCriteria evaluates every typed criterion declared on in.Worker.
// A criterion of no known type passes: the host has nothing to check.
func (s *Service) EvaluateCriteria(ctx context.Context, in DeliveryInput) ([]workerrecords.CriterionOutcome, error) {
	if len(in.Worker.Criteria) == 0 {
		return nil, nil
	}
	changed := collectModifiedPaths(in.ModifiedPaths, in.ChangeReport)
	out := make([]workerrecords.CriterionOutcome, 0, len(in.Worker.Criteria))
	for _, criterion := range in.Worker.Criteria {
		detail, err := s.evaluateOne(ctx, criterion, in, changed)
		if err != nil {
			return nil, err
		}
		out = append(out, workerrecords.CriterionOutcome{Criterion: criterion, Passed: detail == "", Detail: detail})
	}
	return out, nil
}

// evaluateOne returns why criterion did not pass, or "" when it passed.
func (s *Service) evaluateOne(ctx context.Context, criterion string, in DeliveryInput, changed []string) (string, error) {
	switch {
	case criterion == criterionJobComplete:
		if !in.JobComplete {
			return detailRoundIncomplete, nil
		}
	case criterion == criterionSummaryPresent:
		if strings.TrimSpace(in.Summary) == "" {
			return detailSummaryEmpty, nil
		}
	case strings.HasPrefix(criterion, criterionFileModified):
		return s.checkFileModified(ctx, strings.TrimPrefix(criterion, criterionFileModified), in, changed)
	case strings.HasPrefix(criterion, criterionTestPass):
		return s.checkTestPass(ctx, strings.TrimPrefix(criterion, criterionTestPass), in)
	}
	return "", nil
}

// checkFileModified passes when the round changed target, or the workspace
// differs from the plan's base commit at target.
func (s *Service) checkFileModified(ctx context.Context, target string, in DeliveryInput, changed []string) (string, error) {
	cleaned := cleanPaths([]string{target})
	if len(cleaned) == 0 {
		return detailTargetEmpty, nil
	}
	want := cleaned[0]
	for _, path := range changed {
		if singlePathOverlap(path, want) {
			return "", nil
		}
	}
	if in.ProjectDir == "" {
		return detailFileUnmodified, nil
	}
	modified, err := s.gitDiff.HasUncommittedDiff(ctx, in.ProjectDir, want)
	if err != nil {
		return "", err
	}
	if !modified && in.Worker.BaseSHA != "" {
		if modified, err = s.gitDiff.DiffFromRef(ctx, in.ProjectDir, in.Worker.BaseSHA, want); err != nil {
			return "", err
		}
	}
	if !modified {
		return detailFileUnmodified, nil
	}
	return "", nil
}

// checkTestPass passes when the worker's latest run of cmd passed. The
// worker's own receipts are the only evidence.
func (s *Service) checkTestPass(ctx context.Context, cmd string, in DeliveryInput) (string, error) {
	if strings.TrimSpace(cmd) == "" {
		return detailTargetEmpty, nil
	}
	if in.ChildSessionID == "" {
		return detailRunMissing, nil
	}
	runs, err := s.sourceRuns.WorkerSourceRuns(ctx, in.ChildSessionID)
	if err != nil {
		return "", err
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if !commandsurface.SameCommandLine(runs[i].Command, cmd) {
			continue
		}
		if runs[i].Verdict == string(evidence.GateVerdictPassed) {
			return "", nil
		}
		return detailRunFailed, nil
	}
	return detailRunMissing, nil
}

func collectModifiedPaths(diverged []string, report *api.WorkerChangeReport) []string {
	raw := append([]string(nil), diverged...)
	if report != nil {
		raw = append(raw, report.ChangedPaths...)
		for _, stat := range report.DiffStat {
			raw = append(raw, stat.Path)
		}
	}
	return cleanPaths(raw)
}
