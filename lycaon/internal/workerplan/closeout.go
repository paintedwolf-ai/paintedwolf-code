package workerplan

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// CodeCriteriaUnmet is the rejection code for an unsettled plan at closeout.
const CodeCriteriaUnmet = "COORDINATOR_CRITERIA_UNMET"

// Gap names what a closeout check found unsettled.
type Gap string

const (
	// GapUnstarted: a planned worker has not run.
	GapUnstarted Gap = "unstarted"
	// GapRoundActive: the worker's latest round has not settled.
	GapRoundActive Gap = "round_active"
	// GapCriteriaUnmet: the latest result fails typed criteria; Unmet lists them.
	GapCriteriaUnmet Gap = "criteria_unmet"
	// GapChangesPending: the latest result's changes are neither landed nor shelved.
	GapChangesPending Gap = "changes_pending"
	// GapChangesDiscarded: the plan requires file changes that were rejected or shelved.
	GapChangesDiscarded Gap = "changes_discarded"
	// GapScanPending: the landed change's scan obligation is still running.
	GapScanPending Gap = "scan_pending"
	// GapScanFailed: the landed change's scan obligation did not clear.
	GapScanFailed Gap = "scan_failed"
)

// CloseoutVerdict reports whether a session's planned workers are settled.
// A verdict that is not OK names one gap and the worker it concerns.
type CloseoutVerdict struct {
	OK       bool
	Code     string
	Gap      Gap
	LegID    string
	Worker   string
	ResultID string
	// Unmet lists the criteria that did not pass, in plan order.
	Unmet    []string
	Outcomes []workerrecords.CriterionOutcome
	// ScanID names the scan behind a scan gap.
	ScanID string
}

func settled() CloseoutVerdict { return CloseoutVerdict{OK: true} }

func unsettled(w workerrecords.Worker, gap Gap) CloseoutVerdict {
	return CloseoutVerdict{Code: CodeCriteriaUnmet, Gap: gap, LegID: legIdentifier(w), Worker: w.Name}
}

// CheckSessionCloseout reports the first gap among the session's open planned
// or criteria-bearing workers, then the scan gate of its latest landed change.
// workspacePath is where file criteria read diffs.
func (s *Service) CheckSessionCloseout(ctx context.Context, sessionID, workspacePath string) (CloseoutVerdict, error) {
	actor := coordinator(sessionID)
	planned, err := s.plannedWorkers(ctx, actor)
	if err != nil || len(planned) == 0 {
		return settled(), err
	}
	for _, w := range planned {
		verdict, err := s.checkWorkerCloseout(ctx, actor, w, workspacePath)
		if err != nil || !verdict.OK {
			return verdict, err
		}
	}
	hostDir, err := s.hostDataDir(ctx, planned[0].ProjectID)
	if err != nil {
		return CloseoutVerdict{}, err
	}
	err = s.security.CheckSession(ctx, sessionID, hostDir)
	if pending, ok := scan.AsGatePending(err); ok {
		verdict := unsettled(planned[0], GapScanPending)
		verdict.ScanID = pending.ScanID
		return verdict, nil
	}
	if failed, ok := scan.AsGateFailed(err); ok {
		verdict := unsettled(planned[0], GapScanFailed)
		verdict.ScanID = failed.ScanID
		return verdict, nil
	}
	if err != nil {
		return CloseoutVerdict{}, err
	}
	return settled(), nil
}

// plannedWorkers lists the session's open workers that closeout answers for.
func (s *Service) plannedWorkers(ctx context.Context, actor workerrecords.Actor) ([]workerrecords.Worker, error) {
	workers, err := s.records.Workers(ctx, actor)
	if err != nil {
		return nil, err
	}
	var planned []workerrecords.Worker
	for _, w := range workers {
		if w.ClosedAt == "" && (w.BlueprintPath != "" || len(w.Criteria) > 0) {
			planned = append(planned, w)
		}
	}
	return planned, nil
}

func (s *Service) checkWorkerCloseout(ctx context.Context, actor workerrecords.Actor, w workerrecords.Worker, workspacePath string) (CloseoutVerdict, error) {
	rounds, err := s.records.Rounds(ctx, actor, w.ID)
	if err != nil {
		return CloseoutVerdict{}, err
	}
	if len(rounds) == 0 {
		return unsettled(w, GapUnstarted), nil
	}
	last := rounds[len(rounds)-1]
	if last.SettledAt == "" {
		return unsettled(w, GapRoundActive), nil
	}
	job, _ := s.jobs.Get(last.JobID)
	disp, err := s.resultDisposition(ctx, actor, w.ID, last.ResultID)
	if err != nil {
		return CloseoutVerdict{}, err
	}
	// Outcomes recorded at delivery stand; a result delivered before the
	// worker had criteria is evaluated now.
	outcomes := disp.CriteriaOutcomes
	if len(outcomes) == 0 {
		in := DeliveryInput{Worker: w, ChildSessionID: w.ChildSessionID, ProjectDir: workspacePath}
		if job != nil {
			in.JobComplete = job.Status == api.WorkerStatusComplete
			if job.Result != nil {
				in.Summary = job.Result.Summary
				in.ChangeReport = job.Result.ChangeReport
			}
		}
		if outcomes, err = s.EvaluateCriteria(ctx, in); err != nil {
			return CloseoutVerdict{}, err
		}
	}
	verdict := unsettled(w, "")
	verdict.ResultID = last.ResultID
	verdict.Outcomes = outcomes
	if unmet := unmetCriteria(w.Criteria, outcomes); len(unmet) > 0 {
		verdict.Gap, verdict.Unmet = GapCriteriaUnmet, unmet
		return verdict, nil
	}
	if job != nil && api.WorkerTaskOverlayOpen(job) && disp.ChangeState != workerrecords.ChangeShelved {
		verdict.Gap = GapChangesPending
		return verdict, nil
	}
	if requiresFileChanges(w.Criteria) && (disp.ChangeState == workerrecords.ChangeRejected || disp.ChangeState == workerrecords.ChangeShelved) {
		verdict.Gap = GapChangesDiscarded
		return verdict, nil
	}
	return settled(), nil
}

func (s *Service) resultDisposition(ctx context.Context, actor workerrecords.Actor, workerID, resultID string) (workerrecords.Disposition, error) {
	if resultID == "" {
		return workerrecords.Disposition{}, nil
	}
	dispositions, err := s.records.Dispositions(ctx, actor, workerID)
	if err != nil {
		return workerrecords.Disposition{}, err
	}
	for _, d := range dispositions {
		if d.ResultID == resultID {
			return d, nil
		}
	}
	return workerrecords.Disposition{}, nil
}

// SettledWorkflowRunID returns the workflow run of the session's planned
// workers once all of them are settled, and "" until then.
func (s *Service) SettledWorkflowRunID(ctx context.Context, sessionID string) (string, error) {
	verdict, err := s.CheckSessionCloseout(ctx, sessionID, "")
	if err != nil || !verdict.OK {
		return "", err
	}
	planned, err := s.plannedWorkers(ctx, coordinator(sessionID))
	if err != nil {
		return "", err
	}
	for _, w := range planned {
		if w.WorkflowRunID != "" {
			return w.WorkflowRunID, nil
		}
	}
	return "", nil
}

// CloseoutComplete reports whether the session's planned workers are settled.
// A session with none is complete.
func (s *Service) CloseoutComplete(ctx context.Context, sessionID string) (bool, error) {
	verdict, err := s.CheckSessionCloseout(ctx, sessionID, "")
	return verdict.OK, err
}

func legIdentifier(w workerrecords.Worker) string {
	if w.WorkflowWorkID != "" {
		return w.WorkflowWorkID
	}
	return w.Name
}

// unmetCriteria lists the criteria with no passing outcome, in plan order.
func unmetCriteria(criteria []string, outcomes []workerrecords.CriterionOutcome) []string {
	passed := make(map[string]bool, len(outcomes))
	for _, o := range outcomes {
		passed[o.Criterion] = o.Passed
	}
	var unmet []string
	for _, c := range criteria {
		if !passed[c] {
			unmet = append(unmet, c)
		}
	}
	return unmet
}

func requiresFileChanges(criteria []string) bool {
	for _, c := range criteria {
		if strings.HasPrefix(c, criterionFileModified) {
			return true
		}
	}
	return false
}
