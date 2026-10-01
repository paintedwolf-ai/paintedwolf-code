package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

// workerTaskFromRow maps a generated worker_jobs row onto the wire type.
func workerTaskFromRow(ctx context.Context, database db.DBTX, r db.WorkerJobs) (*api.WorkerTask, error) {
	task := api.WorkerTask{
		ID:               r.ID,
		ProjectID:        r.ProjectID,
		WorkspacePath:    r.WorkspacePath,
		ExecutionTarget:  api.ExecutionTarget(r.ExecutionTarget),
		AgentType:        r.AgentType,
		Status:           api.WorkerStatus(r.Status),
		WorkspaceRootID:  db.StringFromNull(r.WorkspaceRootID),
		OverlayID:        db.StringFromNull(r.OverlayID),
		DelegationID:     db.StringFromNull(r.DelegationID),
		LegID:            db.StringFromNull(r.LegID),
		ParentSessionID:  db.StringFromNull(r.ParentSessionID),
		SourceToolCallID: r.SourceToolCallID,
		SourceArgsDigest: r.SourceArgsDigest,
		ChildSessionID:   db.StringFromNull(r.ChildSessionID),
		WorkflowRunID:    db.StringFromNull(r.WorkflowRunID),
		WorkflowPhase:    r.WorkflowPhase,
		WorkflowWorkID:   r.WorkflowWorkID,
		RunnerID:         db.StringFromNull(r.RunnerID),
		ClaimedBy:        db.StringFromNull(r.ClaimedBy),
		ClaimToken:       db.StringFromNull(r.ClaimToken),
		Attempt:          int(r.Attempt),
		Prompt:           r.Prompt,
		Brief:            r.Brief,
		Error:            db.StringFromNull(r.Error),
	}
	if r.SpawnReason.Valid {
		task.SpawnReason = api.SpawnReason(r.SpawnReason.String)
	}
	_ = db.UnmarshalJSON(r.FilesJson, &task.Files)
	var scope api.TaskScope
	if err := db.UnmarshalJSON(r.ScopeJson, &scope); err == nil && r.ScopeJson.Valid && r.ScopeJson.String != "" && r.ScopeJson.String != "null" {
		n := scope.Normalized()
		task.Scope = &n
	}
	_ = db.UnmarshalJSON(r.ResultJson, &task.Result)
	var failure api.WorkerFailure
	if err := db.UnmarshalJSON(r.FailureJson, &failure); err == nil && r.FailureJson.Valid && r.FailureJson.String != "" && r.FailureJson.String != "null" {
		task.Failure = &failure
	}
	if err := resolveWorkerWorkspace(ctx, database, r, &task); err != nil {
		return nil, err
	}
	if ms := db.StringFromNull(r.MergeStatus); ms != "" {
		task.MergeStatus = api.WorkerMergeStatus(ms)
	}
	if r.MaxToolLoops.Valid && r.MaxToolLoops.Int64 > 0 {
		task.MaxToolLoops = int(r.MaxToolLoops.Int64)
	}
	if r.ToolLoopsUsed.Valid && r.ToolLoopsUsed.Int64 >= 0 {
		task.ToolLoopsUsed = int(r.ToolLoopsUsed.Int64)
	}
	if r.ToolCallsUsed.Valid && r.ToolCallsUsed.Int64 >= 0 {
		task.ToolCallsUsed = int(r.ToolCallsUsed.Int64)
	}
	if task.MaxToolLoops <= 0 {
		task.MaxToolLoops = spawn.DefaultWorkerToolBudget().Default
	}
	if r.BudgetRequestJson != "" {
		var req api.WorkerBudgetRequest
		if err := json.Unmarshal([]byte(r.BudgetRequestJson), &req); err != nil {
			return nil, fmt.Errorf("worker job %s budget request: %w", r.ID, err)
		}
		task.BudgetRequest = &req
	}
	var err error
	if err := loadWorkerPrerequisites(ctx, database, &task); err != nil {
		return nil, err
	}
	task.CreatedAt, err = db.ParseTime(r.CreatedAt)
	if err != nil {
		return nil, err
	}
	task.StartedAt, err = db.TimePtrFromNull(r.StartedAt)
	if err != nil {
		return nil, err
	}
	task.HeartbeatAt, err = db.TimePtrFromNull(r.HeartbeatAt)
	if err != nil {
		return nil, err
	}
	task.LeaseExpiresAt, err = db.TimePtrFromNull(r.LeaseExpiresAt)
	if err != nil {
		return nil, err
	}
	task.CompletedAt, err = db.TimePtrFromNull(r.CompletedAt)
	return &task, err
}

func loadWorkerPrerequisites(ctx context.Context, database db.DBTX, task *api.WorkerTask) error {
	rows, err := db.New(database).WorkerPrerequisiteStates(ctx, task.ID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		upstream := api.WorkerTask{Status: api.WorkerStatus(row.Status), MergeStatus: api.WorkerMergeStatus(db.StringFromNull(row.MergeStatus))}
		if err := db.UnmarshalJSON(row.ScopeJson, &upstream.Scope); err != nil {
			return err
		}
		if err := db.UnmarshalJSON(row.ResultJson, &upstream.Result); err != nil {
			return err
		}
		task.AfterWorkers = append(task.AfterWorkers, row.ID)
		task.Dependencies = append(task.Dependencies, api.WorkerDependency{WorkerID: row.ID, State: api.WorkerOutputState(&upstream)})
	}
	return nil
}
