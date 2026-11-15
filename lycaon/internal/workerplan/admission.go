package workerplan

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// RoundAdmissionRequest checks plan, workflow, dependency, and overlap gates before admitting a round.
type RoundAdmissionRequest struct {
	Actor      workerrecords.Actor
	Tool       string
	ProjectID  string
	Worker     workerrecords.Worker
	ScopeMode  string
	WritePaths []string
	AfterNames []string
}

// CheckRoundAdmission enforces Blueprint approval, workflow runnability, and Blueprint write-path overlap.
func (s *Service) CheckRoundAdmission(ctx context.Context, req RoundAdmissionRequest) error {
	if s == nil {
		return nil
	}
	tool := strings.TrimSpace(req.Tool)
	if tool == "" {
		tool = "task"
	}
	if bp := strings.TrimSpace(req.Worker.BlueprintPath); bp != "" {
		if err := s.checkBlueprintApproval(ctx, req.Actor.SessionID, req.ProjectID, bp, tool); err != nil {
			return err
		}
	}
	if runID := strings.TrimSpace(req.Worker.WorkflowRunID); runID != "" && s.Runs != nil {
		if err := s.Runs.AssertRunnable(ctx, runID); err != nil {
			if reject := toolrejection.AsToolReject(err); reject != nil {
				return reject
			}
			return &toolrejection.ToolReject{
				Code: "TOOL_ARGS_INVALID",
				Data: map[string]any{"tool": tool, "workflow_run_id": runID, "reason": err.Error()},
			}
		}
	}
	mode := strings.TrimSpace(req.ScopeMode)
	if mode == "" {
		mode = req.Worker.ScopeMode
	}
	if strings.TrimSpace(req.Worker.BlueprintPath) != "" && mode == workerrecords.ScopeWrite && s.Records != nil {
		paths := req.WritePaths
		if len(paths) == 0 {
			paths = req.Worker.Files
		}
		if err := s.checkBlueprintWriteOverlap(ctx, req.Actor, tool, req.Worker, paths, req.AfterNames); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) checkBlueprintApproval(ctx context.Context, sessionID, projectID, blueprintPath, tool string) error {
	if s.Approvals != nil {
		ok, err := s.Approvals.IsApproved(ctx, projectID, blueprintPath)
		if err != nil {
			if errors.Is(err, blueprint.ErrNotFound) || errors.Is(err, blueprint.ErrInvalidBlueprintPath) {
				return &toolrejection.ToolReject{
					Code: "TOOL_ARGS_INVALID",
					Data: map[string]any{
						"tool":           tool,
						"field":          "blueprint_path",
						"blueprint_path": blueprintPath,
						"reason":         "blueprint_not_found",
					},
				}
			}
			return err
		}
		if !ok {
			return &toolrejection.ToolReject{
				Code: "TOOL_ARGS_INVALID",
				Data: map[string]any{
					"tool":           tool,
					"field":          "blueprint_path",
					"blueprint_path": blueprintPath,
					"reason":         "plan_not_approved",
				},
			}
		}
	}
	if s.WorkflowReady != nil {
		ready, err := s.WorkflowReady.ImplementWorkflowReady(ctx, sessionID, blueprintPath)
		if err != nil {
			return err
		}
		if !ready {
			return &toolrejection.ToolReject{
				Code: "TOOL_ARGS_INVALID",
				Data: map[string]any{
					"tool":           tool,
					"field":          "blueprint_path",
					"blueprint_path": blueprintPath,
					"reason":         "implement_workflow_not_ready",
				},
			}
		}
	}
	return nil
}

func (s *Service) checkActiveWorkflowRun(ctx context.Context, sessionID, tool, blueprintPath string) (string, string, error) {
	if s.ActiveRuns == nil {
		return "", "", nil
	}
	run, err := s.ActiveRuns.ActiveBySession(ctx, sessionID)
	if err != nil || run == nil {
		return "", "", err
	}
	if s.Runs != nil {
		if runErr := s.Runs.AssertRunnable(ctx, run.ID); runErr != nil {
			if reject := toolrejection.AsToolReject(runErr); reject != nil {
				return "", "", reject
			}
			return "", "", &toolrejection.ToolReject{
				Code: "TOOL_ARGS_INVALID",
				Data: map[string]any{
					"tool":            tool,
					"blueprint_path":  blueprintPath,
					"workflow_run_id": run.ID,
					"reason":          runErr.Error(),
				},
			}
		}
	}
	return run.ID, run.CurrentPhase, nil
}

func (s *Service) checkBlueprintWriteOverlap(
	ctx context.Context,
	actor workerrecords.Actor,
	tool string,
	target workerrecords.Worker,
	paths []string,
	afterNames []string,
) error {
	workers, err := s.Records.Workers(ctx, actor)
	if err != nil {
		return err
	}
	byName := make(map[string]string, len(workers))
	depsByWorker := make(map[string][]string, len(workers))
	for _, w := range workers {
		byName[w.Name] = w.ID
		deps, dErr := s.Records.Dependencies(ctx, actor, w.ID)
		if dErr != nil {
			return dErr
		}
		for _, dep := range deps {
			if dep.ProducerWorkerID != "" {
				depsByWorker[w.ID] = append(depsByWorker[w.ID], dep.ProducerWorkerID)
			}
		}
	}
	for _, name := range afterNames {
		if id := byName[strings.TrimSpace(name)]; id != "" {
			depsByWorker[target.ID] = append(depsByWorker[target.ID], id)
		}
	}
	for _, peer := range workers {
		if peer.ID == target.ID || peer.ClosedAt != "" || peer.ScopeMode != workerrecords.ScopeWrite {
			continue
		}
		rounds, rErr := s.Records.Rounds(ctx, actor, peer.ID)
		if rErr != nil {
			return rErr
		}
		if len(rounds) == 0 || rounds[len(rounds)-1].SettledAt != "" {
			continue
		}
		if workerDependsOn(depsByWorker, target.ID, peer.ID) || workerDependsOn(depsByWorker, peer.ID, target.ID) {
			continue
		}
		if PathsOverlap(paths, peer.Files) {
			return &toolrejection.ToolReject{
				Code: "TOOL_ARGS_INVALID",
				Data: map[string]any{
					"tool":           tool,
					"worker":         target.Name,
					"overlap_worker": peer.Name,
					"reason":         "blueprint_write_path_overlap",
				},
			}
		}
	}
	return nil
}

func workerDependsOn(depsByWorker map[string][]string, fromID, targetID string) bool {
	seen := map[string]bool{fromID: true}
	queue := append([]string(nil), depsByWorker[fromID]...)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == targetID {
			return true
		}
		if seen[cur] {
			continue
		}
		seen[cur] = true
		queue = append(queue, depsByWorker[cur]...)
	}
	return false
}

// HasPlannedWorker reports whether sessionID has an open worker bound to workID.
func (s *Service) HasPlannedWorker(ctx context.Context, sessionID, workID string) (bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	workID = strings.TrimSpace(workID)
	if s == nil || s.Records == nil || sessionID == "" || workID == "" {
		return false, nil
	}
	actor := workerrecords.Actor{SessionID: sessionID, Role: workerrecords.RoleCoordinator, PrincipalID: sessionID}
	workers, err := s.Records.Workers(ctx, actor)
	if err != nil {
		return false, err
	}
	for _, w := range workers {
		if w.ClosedAt == "" && w.WorkflowWorkID == workID {
			return true, nil
		}
	}
	return false, nil
}

// ResolvePlannedWorkflowWork supplies TaskIdentityResolver defaults from a seeded planned worker.
func (s *Service) ResolvePlannedWorkflowWork(ctx context.Context, sessionID, workID string) (spawn.WorkflowWork, bool, error) {
	if s == nil || s.Records == nil {
		return spawn.WorkflowWork{}, false, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	workID = strings.TrimSpace(workID)
	if sessionID == "" || workID == "" {
		return spawn.WorkflowWork{}, false, nil
	}
	actor := workerrecords.Actor{SessionID: sessionID, Role: workerrecords.RoleCoordinator, PrincipalID: sessionID}
	w, found, err := s.findPlannedWorker(ctx, actor, workID)
	if err != nil || !found {
		return spawn.WorkflowWork{}, false, err
	}
	scope := &api.TaskScope{Mode: api.TaskScopeMode(w.ScopeMode), Paths: append([]string(nil), w.Files...)}
	charter := &api.WorkerTaskCharter{
		Goal:     w.Brief,
		DoneWhen: append([]string(nil), w.Criteria...),
	}
	return spawn.WorkflowWork{
		RunID:     w.WorkflowRunID,
		Phase:     w.WorkflowPhase,
		AgentType: w.AgentType,
		Scope:     scope,
		Charter:   charter,
	}, true, nil
}

// PreparePlannedTask resolves an unstarted planned worker for workID and returns its prerequisite worker names.
func (s *Service) PreparePlannedTask(
	ctx context.Context,
	actor workerrecords.Actor,
	projectID string,
	workID string,
	scopeMode string,
	writePaths []string,
	explicitAfter []string,
) (workerrecords.Worker, []string, bool, error) {
	if s == nil || s.Records == nil || strings.TrimSpace(workID) == "" {
		return workerrecords.Worker{}, nil, false, nil
	}
	w, found, err := s.findPlannedWorker(ctx, actor, workID)
	if err != nil || !found {
		return workerrecords.Worker{}, nil, false, err
	}
	workers, err := s.Records.Workers(ctx, actor)
	if err != nil {
		return workerrecords.Worker{}, nil, false, err
	}
	byID := make(map[string]workerrecords.Worker, len(workers))
	for _, item := range workers {
		byID[item.ID] = item
	}
	deps, err := s.Records.Dependencies(ctx, actor, w.ID)
	if err != nil {
		return workerrecords.Worker{}, nil, false, err
	}
	mergedAfter := append([]string(nil), explicitAfter...)
	seen := make(map[string]bool, len(mergedAfter))
	for _, name := range mergedAfter {
		seen[strings.TrimSpace(name)] = true
	}
	for _, dep := range deps {
		if dep.ProducerWorkerID == "" {
			continue
		}
		producer, ok := byID[dep.ProducerWorkerID]
		if !ok || producer.ClosedAt != "" {
			continue
		}
		rounds, rErr := s.Records.Rounds(ctx, actor, producer.ID)
		if rErr != nil {
			return workerrecords.Worker{}, nil, false, rErr
		}
		if len(rounds) == 0 {
			return workerrecords.Worker{}, nil, false, &toolrejection.ToolReject{
				Code: "TOOL_ARGS_INVALID",
				Data: map[string]any{
					"tool":                 "task",
					"field":                "workflow_work_id",
					"workflow_work_id":     workID,
					"prerequisite_worker":  producer.Name,
					"prerequisite_work_id": producer.WorkflowWorkID,
					"reason":               "prerequisite_worker_not_started",
				},
			}
		}
		if !seen[producer.Name] {
			seen[producer.Name] = true
			mergedAfter = append(mergedAfter, producer.Name)
		}
	}
	if err := s.CheckRoundAdmission(ctx, RoundAdmissionRequest{
		Actor:      actor,
		Tool:       "task",
		ProjectID:  projectID,
		Worker:     w,
		ScopeMode:  scopeMode,
		WritePaths: writePaths,
		AfterNames: mergedAfter,
	}); err != nil {
		return workerrecords.Worker{}, nil, false, err
	}
	return w, mergedAfter, true, nil
}

// SupersedeSettledWorkflowWorker closes a settled open worker bound to (runID, phase, workID)
// when the workflow admits a fresh replacement worker attempt.
func (s *Service) SupersedeSettledWorkflowWorker(
	ctx context.Context,
	actor workerrecords.Actor,
	op workerrecords.Op,
	runID, phase, workID string,
) error {
	if s == nil || s.Records == nil || strings.TrimSpace(runID) == "" || strings.TrimSpace(workID) == "" {
		return nil
	}
	workers, err := s.Records.Workers(ctx, actor)
	if err != nil {
		return err
	}
	for _, w := range workers {
		if w.ClosedAt != "" || w.WorkflowRunID != runID || w.WorkflowPhase != phase || w.WorkflowWorkID != workID {
			continue
		}
		rounds, rErr := s.Records.Rounds(ctx, actor, w.ID)
		if rErr != nil {
			return rErr
		}
		if len(rounds) == 0 || rounds[len(rounds)-1].SettledAt == "" {
			continue
		}
		if _, cErr := s.Records.Close(ctx, actor, op, w.ID, "superseded"); cErr != nil {
			return cErr
		}
	}
	return nil
}

func (s *Service) findPlannedWorker(ctx context.Context, actor workerrecords.Actor, workID string) (workerrecords.Worker, bool, error) {
	workers, err := s.Records.Workers(ctx, actor)
	if err != nil {
		return workerrecords.Worker{}, false, err
	}
	var activeRunID, activePhase string
	if s.ActiveRuns != nil {
		if run, rErr := s.ActiveRuns.ActiveBySession(ctx, actor.SessionID); rErr == nil && run != nil {
			activeRunID = run.ID
			activePhase = run.CurrentPhase
		}
	}
	var fallback *workerrecords.Worker
	for _, w := range workers {
		if w.ClosedAt != "" || w.WorkflowWorkID != workID {
			continue
		}
		rounds, rErr := s.Records.Rounds(ctx, actor, w.ID)
		if rErr != nil {
			return workerrecords.Worker{}, false, rErr
		}
		if len(rounds) > 0 {
			if w.BlueprintPath != "" {
				return workerrecords.Worker{}, false, &toolrejection.ToolReject{
					Code: "TOOL_ARGS_INVALID",
					Data: map[string]any{
						"tool":             "task",
						"field":            "workflow_work_id",
						"workflow_work_id": workID,
						"worker":           w.Name,
						"reason":           "planned_worker_already_started",
					},
				}
			}
			continue
		}
		if activeRunID != "" && w.WorkflowRunID == activeRunID && w.WorkflowPhase == activePhase {
			return w, true, nil
		}
		if fallback == nil {
			candidate := w
			fallback = &candidate
		}
	}
	if fallback != nil {
		return *fallback, true, nil
	}
	return workerrecords.Worker{}, false, nil
}
