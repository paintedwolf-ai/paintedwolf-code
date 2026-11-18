package workertopology

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

func (r *Runtime) DispatchUnit(ctx context.Context, batchID, unitID string) (*orchestration.TopologyUnit, error) {
	if r == nil || r.Records == nil || r.Queue == nil {
		return nil, fmt.Errorf("topology runtime not configured")
	}
	r.mu.Lock()
	b, ok := r.batches[strings.TrimSpace(batchID)]
	var batchCopy orchestration.TopologyBatch
	var unitCopy orchestration.TopologyUnit
	foundUnit := false
	if ok {
		batchCopy = *b
		for _, u := range r.units[batchID] {
			if u.ID == unitID {
				unitCopy = u
				foundUnit = true
				break
			}
		}
	}
	r.mu.Unlock()
	if !ok || !foundUnit {
		return nil, fmt.Errorf("topology unit %q not found in batch %q", unitID, batchID)
	}
	if batchCopy.Status == orchestration.TopologyBatchAborted {
		return nil, fmt.Errorf("topology batch %q is aborted", batchID)
	}

	mode := api.TaskScopeModeRead
	if strings.TrimSpace(unitCopy.AgentType) == orchestration.ProfileImplementer {
		mode = api.TaskScopeModeWrite
	}
	workflowPhase := ""
	workflowWorkID := ""
	if strings.TrimSpace(batchCopy.WorkflowRunID) != "" {
		workflowPhase = unitCopy.Phase
		workflowWorkID = unitCopy.WorkID
	} else if strings.TrimSpace(batchCopy.BlueprintPath) != "" {
		workflowWorkID = unitCopy.WorkID
	}
	prompt := strings.TrimSpace(unitCopy.Prompt)
	if prompt == "" {
		prompt = strings.TrimSpace(unitCopy.WorkID)
	}
	brief := strings.TrimSpace(unitCopy.WorkID)
	if brief == "" {
		brief = prompt
	}
	task := api.WorkerTask{
		ParentSessionID: batchCopy.SessionID,
		ProjectID:       batchCopy.ProjectID,
		AgentType:       unitCopy.AgentType,
		Brief:           brief,
		Prompt:          prompt,
		Files:           []string{"**/*"},
		Scope:           &api.TaskScope{Mode: mode, Paths: []string{"**/*"}},
		WorkspacePath:   batchCopy.WorkspacePath,
		WorkspaceRoot:   unitCopy.WorkspaceRoot,
		WorkspaceRootID: unitCopy.WorkspaceID,
		WorkflowRunID:   batchCopy.WorkflowRunID,
		WorkflowPhase:   workflowPhase,
		WorkflowWorkID:  workflowWorkID,
		SpawnReason:     api.SpawnReasonInitial,
		Status:          api.WorkerStatusHeld,
		ExecutionTarget: api.ExecutionTargetLocal,
	}
	jobID, err := r.Queue.Enqueue(ctx, task)
	if err != nil {
		return nil, err
	}
	actor := coordinatorActorForSession(batchCopy.SessionID)
	roundOp := workerrecords.Op{ID: "topology:" + batchID + ":round:" + unitID}
	if _, err := r.Records.AdmitRound(ctx, actor, roundOp, unitCopy.WorkerID, workerrecords.RoundSpec{
		JobID:           jobID,
		ExecutionTarget: string(api.ExecutionTargetLocal),
	}); err != nil {
		return nil, err
	}
	if err := r.Queue.ReleaseAdmitted(ctx, jobID); err != nil {
		return nil, err
	}

	r.mu.Lock()
	for i := range r.units[batchID] {
		if r.units[batchID][i].ID == unitID {
			r.units[batchID][i].JobID = jobID
			r.units[batchID][i].Status = api.LegStatusDispatched
			break
		}
	}
	r.mu.Unlock()
	return r.GetUnit(ctx, batchID, unitID)
}

func (r *Runtime) AbortBatch(ctx context.Context, batchID, reason string) error {
	units, err := r.ListUnits(ctx, batchID)
	if err != nil {
		return err
	}
	r.mu.Lock()
	b, ok := r.batches[strings.TrimSpace(batchID)]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("topology batch %q not found", batchID)
	}
	b.Status = computeBatchStatus(units, b.Status)
	if b.Status != orchestration.TopologyBatchActive {
		r.mu.Unlock()
		return nil
	}
	sessionID := b.SessionID
	r.mu.Unlock()

	if strings.TrimSpace(reason) == "" {
		reason = "aborted"
	}
	actor := coordinatorActorForSession(sessionID)
	for _, u := range units {
		if u.Status.IsTerminal() {
			continue
		}
		if u.JobID != "" && r.Queue != nil {
			if err := r.Queue.Cancel(ctx, u.JobID, nil); err != nil {
				return err
			}
			continue
		}
		if u.WorkerID != "" {
			op := workerrecords.Op{ID: "topology:" + batchID + ":abort:" + u.ID}
			if _, err := r.Records.Close(ctx, actor, op, u.WorkerID, reason); err != nil && !errors.Is(err, workerrecords.ErrRoundUnsettled) {
				return err
			}
		}
	}

	r.mu.Lock()
	if cur, exists := r.batches[batchID]; exists && cur.Status == orchestration.TopologyBatchActive {
		cur.Status = orchestration.TopologyBatchAborted
		cur.Reason = strings.TrimSpace(reason)
	}
	r.mu.Unlock()
	return nil
}

func (r *Runtime) WorkflowWorkStatuses(
	ctx context.Context,
	workflowRunID string,
) (map[string]api.LegStatus, error) {
	workflowRunID = strings.TrimSpace(workflowRunID)
	if r == nil || r.Records == nil || workflowRunID == "" {
		return nil, nil
	}
	workers, err := r.Records.ByWorkflowRun(ctx, workflowRunID)
	if err != nil {
		return nil, err
	}
	if len(workers) == 0 {
		return nil, nil
	}
	out := make(map[string]api.LegStatus, len(workers))
	for _, w := range workers {
		if w.WorkflowWorkID == "" || w.ClosedReason == "superseded" {
			continue
		}
		status, _, _, _, _, pErr := r.projectWorkerUnit(ctx, w)
		if pErr != nil {
			return nil, pErr
		}
		out[w.WorkflowWorkID] = status
	}
	return out, nil
}

func (r *Runtime) projectWorkerUnit(
	ctx context.Context,
	w workerrecords.Worker,
) (status api.LegStatus, summary, jobID, wsID, wsRoot string, err error) {
	actor := coordinatorActorForSession(w.SessionID)
	rounds, err := r.Records.Rounds(ctx, actor, w.ID)
	if err != nil {
		return "", "", "", "", "", err
	}
	if len(rounds) == 0 {
		if w.ClosedAt != "" {
			return api.LegStatusCanceled, "", "", "", "", nil
		}
		return api.LegStatusPending, "", "", "", "", nil
	}
	latest := rounds[len(rounds)-1]
	jobID = latest.JobID
	if r.Queue != nil && jobID != "" {
		if task, ok := r.Queue.Get(jobID); ok && task != nil {
			wsID = task.WorkspaceRootID
			wsRoot = task.WorkspaceRoot
			switch task.Status {
			case api.WorkerStatusPending, api.WorkerStatusHeld:
				return api.LegStatusDispatched, "", jobID, wsID, wsRoot, nil
			case api.WorkerStatusRunning, api.WorkerStatusWaiting:
				return api.LegStatusRunning, "", jobID, wsID, wsRoot, nil
			case api.WorkerStatusComplete:
				if task.Result != nil && strings.TrimSpace(task.Result.Status) == "needs_decision" {
					return api.LegStatusRunning, "", jobID, wsID, wsRoot, nil
				}
				if task.Result != nil {
					summary = strings.TrimSpace(task.Result.Summary)
				}
				return api.LegStatusComplete, summary, jobID, wsID, wsRoot, nil
			case api.WorkerStatusFailed:
				return api.LegStatusFailed, "", jobID, wsID, wsRoot, nil
			case api.WorkerStatusCanceled:
				return api.LegStatusCanceled, "", jobID, wsID, wsRoot, nil
			}
		}
	}
	if latest.SettledAt != "" {
		if latest.ResultID != "" {
			return api.LegStatusComplete, "", jobID, wsID, wsRoot, nil
		}
		return api.LegStatusCanceled, "", jobID, wsID, wsRoot, nil
	}
	return api.LegStatusRunning, "", jobID, wsID, wsRoot, nil
}

func computeBatchStatus(units []orchestration.TopologyUnit, current orchestration.TopologyBatchStatus) orchestration.TopologyBatchStatus {
	if current == orchestration.TopologyBatchAborted || len(units) == 0 {
		return current
	}
	anyFailed := false
	for _, u := range units {
		if !u.Status.IsTerminal() {
			return orchestration.TopologyBatchActive
		}
		if u.Status == api.LegStatusFailed || u.Status == api.LegStatusCanceled {
			anyFailed = true
		}
	}
	if anyFailed {
		return orchestration.TopologyBatchFailed
	}
	return orchestration.TopologyBatchDone
}

func coordinatorActorForSession(sessionID string) workerrecords.Actor {
	sessionID = strings.TrimSpace(sessionID)
	return workerrecords.Actor{
		SessionID:   sessionID,
		Role:        workerrecords.RoleCoordinator,
		PrincipalID: sessionID,
	}
}
