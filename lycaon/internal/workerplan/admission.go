package workerplan

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// RoundAdmissionRequest is one round about to be admitted for Worker.
type RoundAdmissionRequest struct {
	Actor      workerrecords.Actor
	Tool       string
	ProjectID  string
	Worker     workerrecords.Worker
	ScopeMode  string
	WritePaths []string
	AfterNames []string
}

// CheckRoundAdmission enforces Blueprint approval, workflow runnability and
// write-path overlap between Blueprint workers.
func (s *Service) CheckRoundAdmission(ctx context.Context, req RoundAdmissionRequest) error {
	w := req.Worker
	if w.BlueprintPath != "" {
		if err := s.checkBlueprintApproval(ctx, req.Actor.SessionID, req.ProjectID, w.BlueprintPath, req.Tool); err != nil {
			return err
		}
	}
	if w.WorkflowRunID != "" {
		if err := s.checkRunnable(ctx, w.WorkflowRunID, req.Tool); err != nil {
			return err
		}
	}
	mode := req.ScopeMode
	if mode == "" {
		mode = w.ScopeMode
	}
	if w.BlueprintPath == "" || mode != workerrecords.ScopeWrite {
		return nil
	}
	paths := req.WritePaths
	if len(paths) == 0 {
		paths = w.Files
	}
	return s.checkBlueprintWriteOverlap(ctx, req.Actor, req.Tool, w, paths, req.AfterNames)
}

func planReject(tool, reason string, data map[string]any) *toolrejection.ToolReject {
	out := map[string]any{"tool": tool, "reason": reason}
	for k, v := range data {
		out[k] = v
	}
	return &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: out}
}

func blueprintReject(tool, reason, blueprintPath string) *toolrejection.ToolReject {
	data := map[string]any{"field": "blueprint_path"}
	if blueprintPath != "" {
		data["blueprint_path"] = blueprintPath
	}
	return planReject(tool, reason, data)
}

func (s *Service) checkBlueprintApproval(ctx context.Context, sessionID, projectID, blueprintPath, tool string) error {
	approved, err := s.approvals.IsApproved(ctx, projectID, blueprintPath)
	if errors.Is(err, blueprint.ErrNotFound) || errors.Is(err, blueprint.ErrInvalidBlueprintPath) {
		return blueprintReject(tool, "blueprint_not_found", blueprintPath)
	}
	if err != nil {
		return err
	}
	if !approved {
		return blueprintReject(tool, "plan_not_approved", blueprintPath)
	}
	ready, err := s.workflowReady.ImplementWorkflowReady(ctx, sessionID, blueprintPath)
	if err != nil {
		return err
	}
	if !ready {
		return blueprintReject(tool, "implement_workflow_not_ready", blueprintPath)
	}
	return nil
}

// checkRunnable refuses a round on a workflow run that is paused or finished.
func (s *Service) checkRunnable(ctx context.Context, runID, tool string) error {
	err := s.runs.AssertRunnable(ctx, runID)
	if err == nil {
		return nil
	}
	if reject := toolrejection.AsToolReject(err); reject != nil {
		return reject
	}
	var stopped *runstate.NotRunnableError
	if errors.As(err, &stopped) {
		return planReject(tool, "workflow_run_not_runnable", map[string]any{
			"workflow_run_id": runID, "workflow_run_status": string(stopped.Status),
		})
	}
	return err
}

// activeRun returns the session's active workflow run and phase after
// checking the run can take work. Both are empty when no run is active.
func (s *Service) activeRun(ctx context.Context, sessionID, tool string) (runID, phase string, err error) {
	run, err := s.activeRuns.ActiveBySession(ctx, sessionID)
	if err != nil || run == nil {
		return "", "", err
	}
	if err := s.checkRunnable(ctx, run.ID, tool); err != nil {
		return "", "", err
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
	workers, err := s.records.Workers(ctx, actor)
	if err != nil {
		return err
	}
	byName := make(map[string]string, len(workers))
	depsByWorker := make(map[string][]string, len(workers))
	for _, w := range workers {
		byName[w.Name] = w.ID
		deps, err := s.records.Dependencies(ctx, actor, w.ID)
		if err != nil {
			return err
		}
		for _, dep := range deps {
			if dep.ProducerWorkerID != "" {
				depsByWorker[w.ID] = append(depsByWorker[w.ID], dep.ProducerWorkerID)
			}
		}
	}
	for _, name := range afterNames {
		if id := byName[name]; id != "" {
			depsByWorker[target.ID] = append(depsByWorker[target.ID], id)
		}
	}
	for _, peer := range workers {
		if peer.ID == target.ID || peer.ClosedAt != "" || peer.ScopeMode != workerrecords.ScopeWrite {
			continue
		}
		rounds, err := s.records.Rounds(ctx, actor, peer.ID)
		if err != nil {
			return err
		}
		if len(rounds) == 0 || rounds[len(rounds)-1].SettledAt != "" {
			continue
		}
		// Ordered workers may share paths; only concurrent ones collide.
		if workerDependsOn(depsByWorker, target.ID, peer.ID) || workerDependsOn(depsByWorker, peer.ID, target.ID) {
			continue
		}
		if PathsOverlap(paths, peer.Files) {
			return planReject(tool, "blueprint_write_path_overlap", map[string]any{
				"worker": target.Name, "overlap_worker": peer.Name,
			})
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
	if workID == "" {
		return false, nil
	}
	workers, err := s.records.Workers(ctx, coordinator(sessionID))
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

// ResolvePlannedWorkflowWork returns the workflow binding, agent, scope and
// charter of the unstarted planned worker for workID, if the session has one.
func (s *Service) ResolvePlannedWorkflowWork(ctx context.Context, sessionID, workID string) (spawn.WorkflowWork, bool, error) {
	w, found, err := s.findPlannedWorker(ctx, coordinator(sessionID), workID)
	if err != nil || !found {
		return spawn.WorkflowWork{}, false, err
	}
	return spawn.WorkflowWork{
		RunID:     w.WorkflowRunID,
		Phase:     w.WorkflowPhase,
		AgentType: w.AgentType,
		Scope:     &api.TaskScope{Mode: api.TaskScopeMode(w.ScopeMode), Paths: append([]string(nil), w.Files...)},
		Charter:   &api.WorkerTaskCharter{Goal: w.Brief, DoneWhen: append([]string(nil), w.Criteria...)},
	}, true, nil
}

// PreparePlannedTask admits the first round of the unstarted planned worker
// for workID. It returns the worker and the workers it must run after: the
// caller's, then the plan's own prerequisites. found is false when the
// session has no planned worker for workID.
func (s *Service) PreparePlannedTask(
	ctx context.Context,
	actor workerrecords.Actor,
	projectID string,
	workID string,
	scopeMode string,
	writePaths []string,
	explicitAfter []string,
) (w workerrecords.Worker, after []string, found bool, err error) {
	w, found, err = s.findPlannedWorker(ctx, actor, workID)
	if err != nil || !found {
		return workerrecords.Worker{}, nil, false, err
	}
	workers, err := s.records.Workers(ctx, actor)
	if err != nil {
		return workerrecords.Worker{}, nil, false, err
	}
	byID := make(map[string]workerrecords.Worker, len(workers))
	for _, item := range workers {
		byID[item.ID] = item
	}
	deps, err := s.records.Dependencies(ctx, actor, w.ID)
	if err != nil {
		return workerrecords.Worker{}, nil, false, err
	}
	after = append([]string(nil), explicitAfter...)
	seen := make(map[string]bool, len(after))
	for _, name := range after {
		seen[name] = true
	}
	for _, dep := range deps {
		producer, ok := byID[dep.ProducerWorkerID]
		if !ok || producer.ClosedAt != "" {
			continue
		}
		rounds, err := s.records.Rounds(ctx, actor, producer.ID)
		if err != nil {
			return workerrecords.Worker{}, nil, false, err
		}
		if len(rounds) == 0 {
			return workerrecords.Worker{}, nil, false, planReject("task", "prerequisite_worker_not_started", map[string]any{
				"field": "workflow_work_id", "workflow_work_id": workID,
				"prerequisite_worker": producer.Name, "prerequisite_work_id": producer.WorkflowWorkID,
			})
		}
		if !seen[producer.Name] {
			seen[producer.Name] = true
			after = append(after, producer.Name)
		}
	}
	if err := s.CheckRoundAdmission(ctx, RoundAdmissionRequest{
		Actor: actor, Tool: "task", ProjectID: projectID, Worker: w,
		ScopeMode: scopeMode, WritePaths: writePaths, AfterNames: after,
	}); err != nil {
		return workerrecords.Worker{}, nil, false, err
	}
	return w, after, true, nil
}

// SupersedeSettledWorkflowWorker closes the settled open worker bound to
// (runID, phase, workID) so the workflow can start a fresh attempt.
func (s *Service) SupersedeSettledWorkflowWorker(
	ctx context.Context,
	actor workerrecords.Actor,
	op workerrecords.Op,
	runID, phase, workID string,
) error {
	workers, err := s.records.Workers(ctx, actor)
	if err != nil {
		return err
	}
	for _, w := range workers {
		if w.ClosedAt != "" || w.WorkflowRunID != runID || w.WorkflowPhase != phase || w.WorkflowWorkID != workID {
			continue
		}
		rounds, err := s.records.Rounds(ctx, actor, w.ID)
		if err != nil {
			return err
		}
		if len(rounds) == 0 || rounds[len(rounds)-1].SettledAt == "" {
			continue
		}
		if _, err := s.records.Close(ctx, actor, op, w.ID, "superseded"); err != nil {
			return err
		}
	}
	return nil
}

// findPlannedWorker resolves workID to the session's unstarted planned
// worker. A worker is planned under the session's active workflow run, or
// under none when no run is active; the active phase decides between several.
// Refused, each with its own reason: a Blueprint worker that already ran,
// whose next round goes through instruct_worker; a Blueprint worker planned
// under another run, which plan_from_blueprint moves to this one; and a work
// ID that still names several workers.
func (s *Service) findPlannedWorker(ctx context.Context, actor workerrecords.Actor, workID string) (workerrecords.Worker, bool, error) {
	if workID == "" {
		return workerrecords.Worker{}, false, nil
	}
	workers, err := s.records.Workers(ctx, actor)
	if err != nil {
		return workerrecords.Worker{}, false, err
	}
	var runID, phase string
	run, err := s.activeRuns.ActiveBySession(ctx, actor.SessionID)
	if err != nil {
		return workerrecords.Worker{}, false, err
	}
	if run != nil {
		runID, phase = run.ID, run.CurrentPhase
	}
	refuse := func(reason string, names ...string) (workerrecords.Worker, bool, error) {
		data := map[string]any{"field": "workflow_work_id", "workflow_work_id": workID}
		if len(names) == 1 {
			data["worker"] = names[0]
		} else {
			data["workers"] = names
		}
		return workerrecords.Worker{}, false, planReject("task", reason, data)
	}
	var candidates, elsewhere []workerrecords.Worker
	for _, w := range workers {
		if w.ClosedAt != "" || w.WorkflowWorkID != workID {
			continue
		}
		rounds, err := s.records.Rounds(ctx, actor, w.ID)
		if err != nil {
			return workerrecords.Worker{}, false, err
		}
		switch {
		case len(rounds) > 0 && w.BlueprintPath != "":
			return refuse("planned_worker_already_started", w.Name)
		case len(rounds) > 0:
		case w.WorkflowRunID == runID:
			candidates = append(candidates, w)
		case w.BlueprintPath != "":
			elsewhere = append(elsewhere, w)
		}
	}
	if len(candidates) > 1 {
		var inPhase []workerrecords.Worker
		for _, w := range candidates {
			if w.WorkflowPhase == phase {
				inPhase = append(inPhase, w)
			}
		}
		if len(inPhase) != 1 {
			return refuse("planned_worker_ambiguous", workerNames(candidates)...)
		}
		candidates = inPhase
	}
	if len(candidates) == 1 {
		return candidates[0], true, nil
	}
	if len(elsewhere) > 0 {
		return refuse("planned_worker_other_run", workerNames(elsewhere)...)
	}
	return workerrecords.Worker{}, false, nil
}

func workerNames(workers []workerrecords.Worker) []string {
	names := make([]string, len(workers))
	for i, w := range workers {
		names[i] = w.Name
	}
	return names
}
