package workerplan

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// CloseoutVerdict reports whether a coordinator session's planned legs and typed criteria are settled.
type CloseoutVerdict struct {
	OK          bool
	Code        string
	LegID       string
	Worker      string
	ResultID    string
	Reason      string
	Outcomes    []workerrecords.CriterionOutcome
	GatePending *scan.GatePendingError
}

// CheckSessionCloseout verifies that every open planned or criteria-bearing worker in sessionID
// has delivered a result whose typed criteria and required evidence are satisfied.
func (s *Service) CheckSessionCloseout(
	ctx context.Context,
	sessionID string,
	projectID string,
	workspacePath string,
) (CloseoutVerdict, error) {
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || s.Records == nil || sessionID == "" {
		return CloseoutVerdict{OK: true}, nil
	}
	actor := workerrecords.Actor{
		SessionID:   sessionID,
		Role:        workerrecords.RoleCoordinator,
		PrincipalID: sessionID,
	}
	workers, err := s.Records.Workers(ctx, actor)
	if err != nil {
		return CloseoutVerdict{}, err
	}
	var planned []workerrecords.Worker
	for _, w := range workers {
		if w.ClosedAt != "" {
			continue
		}
		if w.BlueprintPath != "" || len(w.Criteria) > 0 {
			planned = append(planned, w)
		}
	}
	if len(planned) == 0 {
		return CloseoutVerdict{OK: true}, nil
	}
	for _, w := range planned {
		if projectID == "" {
			projectID = w.ProjectID
		}
		verdict, vErr := s.checkWorkerCloseout(ctx, actor, w, workspacePath)
		if vErr != nil {
			return CloseoutVerdict{}, vErr
		}
		if !verdict.OK {
			return verdict, nil
		}
	}
	if s.Security != nil && s.HostDataDir != nil && strings.TrimSpace(projectID) != "" {
		hostDir, hErr := s.HostDataDir(ctx, projectID)
		if hErr != nil {
			return CloseoutVerdict{}, hErr
		}
		if secErr := s.Security.CheckSession(ctx, sessionID, hostDir); secErr != nil {
			legID := legIdentifier(planned[0])
			if pending, ok := scan.AsGatePending(secErr); ok {
				return CloseoutVerdict{
					OK:          false,
					Code:        "COORDINATOR_CRITERIA_UNMET",
					LegID:       legID,
					Worker:      planned[0].Name,
					Reason:      pending.Reason,
					GatePending: pending,
				}, nil
			}
			return CloseoutVerdict{
				OK:     false,
				Code:   "COORDINATOR_CRITERIA_UNMET",
				LegID:  legID,
				Worker: planned[0].Name,
				Reason: secErr.Error(),
			}, nil
		}
	}
	return CloseoutVerdict{OK: true}, nil
}

func (s *Service) checkWorkerCloseout(
	ctx context.Context,
	actor workerrecords.Actor,
	w workerrecords.Worker,
	workspacePath string,
) (CloseoutVerdict, error) {
	legID := legIdentifier(w)
	rounds, err := s.Records.Rounds(ctx, actor, w.ID)
	if err != nil {
		return CloseoutVerdict{}, err
	}
	if len(rounds) == 0 {
		return CloseoutVerdict{
			OK:     false,
			Code:   "COORDINATOR_CRITERIA_UNMET",
			LegID:  legID,
			Worker: w.Name,
			Reason: fmt.Sprintf("worker %s (leg %s) has not run; criteria unmet: %s", w.Name, legID, strings.Join(w.Criteria, ", ")),
		}, nil
	}
	last := rounds[len(rounds)-1]
	if last.SettledAt == "" {
		return CloseoutVerdict{
			OK:     false,
			Code:   "COORDINATOR_CRITERIA_UNMET",
			LegID:  legID,
			Worker: w.Name,
			Reason: fmt.Sprintf("worker %s (leg %s) round %d is still active", w.Name, legID, last.Ordinal),
		}, nil
	}
	var job *api.WorkerTask
	if s.Jobs != nil {
		if found, ok := s.Jobs.Get(last.JobID); ok {
			job = found
		}
		if job == nil || job.Status != api.WorkerStatusComplete {
			return CloseoutVerdict{
				OK:       false,
				Code:     "COORDINATOR_UNGROUNDED_CLAIM",
				LegID:    legID,
				Worker:   w.Name,
				ResultID: last.ResultID,
				Reason:   fmt.Sprintf("worker %s (leg %s) job not complete on ledger", w.Name, legID),
			}, nil
		}
	}
	disps, err := s.Records.Dispositions(ctx, actor, w.ID)
	if err != nil {
		return CloseoutVerdict{}, err
	}
	var disp workerrecords.Disposition
	for _, item := range disps {
		if item.ResultID == last.ResultID && last.ResultID != "" {
			disp = item
			break
		}
	}
	outcomes := disp.CriteriaOutcomes
	if len(outcomes) == 0 && len(w.Criteria) > 0 {
		jobComplete := false
		summary := ""
		var report *api.WorkerChangeReport
		if job != nil {
			jobComplete = job.Status == api.WorkerStatusComplete
			if job.Result != nil {
				summary = job.Result.Summary
				report = job.Result.ChangeReport
			}
		}
		outcomes = s.EvaluateCriteria(ctx, DeliveryInput{
			Worker:         w,
			JobID:          last.JobID,
			ChildSessionID: w.ChildSessionID,
			ProjectDir:     workspacePath,
			JobComplete:    jobComplete,
			Summary:        summary,
			ChangeReport:   report,
		})
	}
	if !allCriteriaPassed(w.Criteria, outcomes) {
		code := "COORDINATOR_CRITERIA_UNMET"
		if ungroundedCriterionFailed(w.Criteria, outcomes) {
			code = "COORDINATOR_UNGROUNDED_CLAIM"
		}
		return CloseoutVerdict{
			OK:       false,
			Code:     code,
			LegID:    legID,
			Worker:   w.Name,
			ResultID: last.ResultID,
			Outcomes: outcomes,
			Reason:   formatCriteriaOutcomes(w.Name, last.ResultID, w.Criteria, outcomes),
		}, nil
	}
	if job != nil && api.WorkerTaskOverlayOpen(job) && disp.ChangeState != workerrecords.ChangeShelved {
		return CloseoutVerdict{
			OK:       false,
			Code:     "COORDINATOR_CRITERIA_UNMET",
			LegID:    legID,
			Worker:   w.Name,
			ResultID: last.ResultID,
			Outcomes: outcomes,
			Reason:   fmt.Sprintf("worker %s result %s changes have not landed", w.Name, last.ResultID),
		}, nil
	}
	if hasFileModifiedCriterion(w.Criteria) && (disp.ChangeState == workerrecords.ChangeRejected || disp.ChangeState == workerrecords.ChangeShelved) {
		return CloseoutVerdict{
			OK:       false,
			Code:     "COORDINATOR_CRITERIA_UNMET",
			LegID:    legID,
			Worker:   w.Name,
			ResultID: last.ResultID,
			Outcomes: outcomes,
			Reason:   fmt.Sprintf("worker %s result %s changes were %s; required file modifications have not landed", w.Name, last.ResultID, disp.ChangeState),
		}, nil
	}
	return CloseoutVerdict{OK: true}, nil
}

// SettledWorkflowRunID returns the workflow run ID bound to sessionID's settled plan legs, if any.
func (s *Service) SettledWorkflowRunID(ctx context.Context, sessionID string) string {
	verdict, err := s.CheckSessionCloseout(ctx, sessionID, "", "")
	if err != nil || !verdict.OK || s == nil || s.Records == nil {
		return ""
	}
	actor := workerrecords.Actor{
		SessionID:   strings.TrimSpace(sessionID),
		Role:        workerrecords.RoleCoordinator,
		PrincipalID: strings.TrimSpace(sessionID),
	}
	workers, err := s.Records.Workers(ctx, actor)
	if err != nil {
		return ""
	}
	for _, w := range workers {
		if w.ClosedAt == "" && (w.BlueprintPath != "" || len(w.Criteria) > 0) && strings.TrimSpace(w.WorkflowRunID) != "" {
			return strings.TrimSpace(w.WorkflowRunID)
		}
	}
	return ""
}

// CloseoutComplete builds the delegation_closeout_complete condition callback over workerrecords.
func (s *Service) CloseoutComplete() func(context.Context, string) (bool, error) {
	return func(ctx context.Context, sessionID string) (bool, error) {
		if s == nil || s.Records == nil {
			return true, nil
		}
		actor := workerrecords.Actor{
			SessionID:   strings.TrimSpace(sessionID),
			Role:        workerrecords.RoleCoordinator,
			PrincipalID: strings.TrimSpace(sessionID),
		}
		workers, err := s.Records.Workers(ctx, actor)
		if err != nil {
			return false, err
		}
		hasPlan := false
		for _, w := range workers {
			if w.ClosedAt == "" && (w.BlueprintPath != "" || len(w.Criteria) > 0) {
				hasPlan = true
				break
			}
		}
		if !hasPlan {
			return true, nil
		}
		verdict, err := s.CheckSessionCloseout(ctx, sessionID, "", "")
		if err != nil {
			return false, err
		}
		return verdict.OK, nil
	}
}

// SessionWorkIDs returns active or completed planned work identities for sessionID.
func (s *Service) SessionWorkIDs(ctx context.Context, sessionID string) ([]string, error) {
	if s == nil || s.Records == nil || strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	actor := workerrecords.Actor{
		SessionID:   strings.TrimSpace(sessionID),
		Role:        workerrecords.RoleCoordinator,
		PrincipalID: strings.TrimSpace(sessionID),
	}
	workers, err := s.Records.Workers(ctx, actor)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, w := range workers {
		if w.ClosedAt != "" {
			continue
		}
		if w.State != workerrecords.WorkerWorking && w.State != workerrecords.WorkerWaiting &&
			w.State != workerrecords.WorkerAsking && w.State != workerrecords.WorkerSettled {
			continue
		}
		out = append(out, legIdentifier(w))
	}
	return out, nil
}

func ungroundedCriterionFailed(criteria []string, outcomes []workerrecords.CriterionOutcome) bool {
	byName := make(map[string]bool, len(outcomes))
	for _, o := range outcomes {
		byName[strings.TrimSpace(o.Criterion)] = o.Passed
	}
	for _, raw := range criteria {
		c := strings.TrimSpace(raw)
		if (c == "job:complete" || c == "summary:present") && !byName[c] {
			return true
		}
	}
	return false
}

func legIdentifier(w workerrecords.Worker) string {
	if strings.TrimSpace(w.WorkflowWorkID) != "" {
		return strings.TrimSpace(w.WorkflowWorkID)
	}
	return w.Name
}

func allCriteriaPassed(criteria []string, outcomes []workerrecords.CriterionOutcome) bool {
	if len(criteria) == 0 {
		return true
	}
	byName := make(map[string]bool, len(outcomes))
	for _, o := range outcomes {
		byName[strings.TrimSpace(o.Criterion)] = o.Passed
	}
	for _, raw := range criteria {
		c := strings.TrimSpace(raw)
		if c != "" && !byName[c] {
			return false
		}
	}
	return true
}

func formatCriteriaOutcomes(workerName, resultID string, criteria []string, outcomes []workerrecords.CriterionOutcome) string {
	byName := make(map[string]workerrecords.CriterionOutcome, len(outcomes))
	for _, o := range outcomes {
		byName[strings.TrimSpace(o.Criterion)] = o
	}
	parts := make([]string, 0, len(criteria))
	for _, raw := range criteria {
		c := strings.TrimSpace(raw)
		if c == "" {
			continue
		}
		o, ok := byName[c]
		switch {
		case !ok:
			parts = append(parts, c+"=unmet")
		case o.Passed:
			parts = append(parts, c+"=passed")
		case strings.TrimSpace(o.Detail) != "":
			parts = append(parts, fmt.Sprintf("%s=failed (%s)", c, strings.TrimSpace(o.Detail)))
		default:
			parts = append(parts, c+"=failed")
		}
	}
	prefix := "worker " + workerName
	if strings.TrimSpace(resultID) != "" {
		prefix += " result " + strings.TrimSpace(resultID)
	}
	return prefix + " criteria outcomes: " + strings.Join(parts, ", ")
}

func hasFileModifiedCriterion(criteria []string) bool {
	for _, c := range criteria {
		if strings.HasPrefix(strings.TrimSpace(c), "file:modified:") {
			return true
		}
	}
	return false
}
