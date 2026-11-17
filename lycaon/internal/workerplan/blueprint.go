package workerplan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerrecords"
)

// SeededLeg describes one reconciled Blueprint leg in the session roster.
type SeededLeg struct {
	WorkID       string   `json:"work_id"`
	Worker       string   `json:"worker"`
	AgentType    string   `json:"agent_type"`
	ScopeMode    string   `json:"scope_mode"`
	Files        []string `json:"files,omitempty"`
	AfterWorkers []string `json:"after_workers,omitempty"`
	Criteria     []string `json:"criteria"`
	State        string   `json:"state"`
}

// SeedResult reports the outcome of plan_from_blueprint.
type SeedResult struct {
	BlueprintPath     string      `json:"blueprint_path"`
	BlueprintRevision string      `json:"blueprint_revision"`
	BaseSHA           string      `json:"base_sha,omitempty"`
	WorkflowRunID     string      `json:"workflow_run_id,omitempty"`
	WorkflowPhase     string      `json:"workflow_phase,omitempty"`
	Legs              []SeededLeg `json:"legs"`
	Withdrawn         []string    `json:"withdrawn,omitempty"`
}

// SeedRequest carries caller inputs to PlanFromBlueprint.
type SeedRequest struct {
	Actor         workerrecords.Actor
	OperationID   string
	ProjectID     string
	WorkspacePath string
	BlueprintPath string
}

// PlanFromBlueprint reconciles the session's planned workers with an approved
// Blueprint: it creates a worker per new task, restages unstarted ones whose
// task changed and withdraws unstarted ones whose task is gone. Workers that
// already ran are left as they are.
func (s *Service) PlanFromBlueprint(ctx context.Context, req SeedRequest) (SeedResult, error) {
	const tool = "plan_from_blueprint"
	path := strings.TrimSpace(req.BlueprintPath)
	if path == "" {
		return SeedResult{}, blueprintReject(tool, "blueprint_path_required", "")
	}
	planDoc, err := s.plans.Get(ctx, req.ProjectID, path)
	if errors.Is(err, blueprint.ErrNotFound) || errors.Is(err, blueprint.ErrInvalidBlueprintPath) {
		return SeedResult{}, blueprintReject(tool, "blueprint_not_found", path)
	}
	if err != nil {
		return SeedResult{}, err
	}
	if err := s.checkBlueprintApproval(ctx, req.Actor.SessionID, req.ProjectID, path, tool); err != nil {
		return SeedResult{}, err
	}
	runID, phase, err := s.activeRun(ctx, req.Actor.SessionID, tool)
	if err != nil {
		return SeedResult{}, err
	}
	legs, err := ParseBlueprintLegs(planDoc)
	var graph *GraphError
	if errors.As(err, &graph) {
		reject := blueprintReject(tool, graph.Reason, path)
		if graph.Task != "" {
			reject.Data["task"] = graph.Task
		}
		if graph.Other != "" {
			reject.Data["other_task"] = graph.Other
		}
		return SeedResult{}, reject
	}
	if err != nil {
		return SeedResult{}, err
	}
	revision := blueprint.ContentDigest(planDoc.Content)
	existing, err := s.records.ByBlueprint(ctx, req.Actor, path)
	if err != nil {
		return SeedResult{}, err
	}
	baseSHA := existingBaseSHA(existing)
	if baseSHA == "" && req.WorkspacePath != "" {
		// A workspace without a commit has no base; file criteria then rest
		// on each round's change report.
		if sha, err := s.headSHA.HeadSHA(ctx, req.WorkspacePath); err == nil {
			baseSHA = strings.TrimSpace(sha)
		}
	}
	return s.reconcileBlueprintLegs(ctx, req, path, revision, baseSHA, runID, phase, legs, existing)
}

func (s *Service) reconcileBlueprintLegs(
	ctx context.Context,
	req SeedRequest,
	blueprintPath, revision, baseSHA, runID, phase string,
	legs []LegSpec,
	existing []workerrecords.Worker,
) (SeedResult, error) {
	sessionWorkers, err := s.records.Workers(ctx, req.Actor)
	if err != nil {
		return SeedResult{}, err
	}
	takenNames := make(map[string]bool, len(sessionWorkers))
	for _, w := range sessionWorkers {
		takenNames[w.Name] = true
	}
	wantByWorkID := make(map[string]LegSpec, len(legs))
	for _, leg := range legs {
		wantByWorkID[leg.WorkID] = leg
	}
	openByWorkID := make(map[string]workerrecords.Worker, len(existing))
	roundsByWorker := make(map[string][]workerrecords.Round, len(existing))
	var withdrawn []string

	for _, w := range existing {
		if w.ClosedAt != "" {
			continue
		}
		rounds, rErr := s.records.Rounds(ctx, req.Actor, w.ID)
		if rErr != nil {
			return SeedResult{}, rErr
		}
		roundsByWorker[w.ID] = rounds
		if _, wanted := wantByWorkID[w.WorkflowWorkID]; !wanted && len(rounds) == 0 {
			op := workerrecords.Op{ID: fmt.Sprintf("%s:withdraw:%s", req.OperationID, w.ID)}
			if _, cErr := s.records.Close(ctx, req.Actor, op, w.ID, "withdrawn"); cErr != nil {
				return SeedResult{}, cErr
			}
			withdrawn = append(withdrawn, w.Name)
			continue
		}
		openByWorkID[w.WorkflowWorkID] = w
	}

	unchanged := make(map[string]bool, len(legs))
	for _, leg := range legs {
		spec := legWorkerSpec(leg, blueprintPath, revision, baseSHA, runID, phase)
		if cur, ok := openByWorkID[leg.WorkID]; ok {
			if len(roundsByWorker[cur.ID]) > 0 {
				continue
			}
			if cur.BlueprintRevision == revision && cur.WorkflowRunID == runID && cur.WorkflowPhase == phase {
				unchanged[cur.ID] = true
				continue
			}
			op := workerrecords.Op{ID: fmt.Sprintf("%s:stage:%s", req.OperationID, leg.WorkID)}
			updated, uErr := s.records.UpdatePlanned(ctx, req.Actor, op, cur.ID, spec, nil)
			if uErr != nil {
				return SeedResult{}, uErr
			}
			openByWorkID[leg.WorkID] = updated
			continue
		}
		if !takenNames[leg.WorkID] {
			spec.Name = leg.WorkID
		}
		op := workerrecords.Op{ID: fmt.Sprintf("%s:create:%s", req.OperationID, leg.WorkID)}
		created, cErr := s.records.CreateWorker(ctx, req.Actor, op, spec)
		if cErr != nil {
			return SeedResult{}, cErr
		}
		takenNames[created.Name] = true
		w, wErr := s.records.Worker(ctx, req.Actor, created.WorkerID)
		if wErr != nil {
			return SeedResult{}, wErr
		}
		openByWorkID[leg.WorkID] = w
	}

	outLegs := make([]SeededLeg, 0, len(legs))
	for _, leg := range legs {
		w := openByWorkID[leg.WorkID]
		rounds := roundsByWorker[w.ID]
		afterNames := make([]string, 0, len(leg.DependsOn))
		for _, depID := range leg.DependsOn {
			if producer, ok := openByWorkID[depID]; ok {
				afterNames = append(afterNames, producer.Name)
			}
		}
		slices.Sort(afterNames)
		if len(rounds) == 0 && !unchanged[w.ID] && len(afterNames) > 0 {
			spec := legWorkerSpec(leg, blueprintPath, revision, baseSHA, runID, phase)
			op := workerrecords.Op{ID: fmt.Sprintf("%s:deps:%s", req.OperationID, leg.WorkID)}
			updated, uErr := s.records.UpdatePlanned(ctx, req.Actor, op, w.ID, spec, afterNames)
			if uErr != nil {
				return SeedResult{}, uErr
			}
			w = updated
		}
		outLegs = append(outLegs, SeededLeg{
			WorkID:       leg.WorkID,
			Worker:       w.Name,
			AgentType:    w.AgentType,
			ScopeMode:    w.ScopeMode,
			Files:        append([]string(nil), w.Files...),
			AfterWorkers: afterNames,
			Criteria:     append([]string(nil), w.Criteria...),
			State:        legRunState(rounds),
		})
	}
	return SeedResult{
		BlueprintPath:     blueprintPath,
		BlueprintRevision: revision,
		BaseSHA:           baseSHA,
		WorkflowRunID:     runID,
		WorkflowPhase:     phase,
		Legs:              outLegs,
		Withdrawn:         withdrawn,
	}, nil
}

func legWorkerSpec(leg LegSpec, blueprintPath, revision, baseSHA, runID, phase string) workerrecords.WorkerSpec {
	return workerrecords.WorkerSpec{
		AgentType:         leg.AgentType,
		ScopeMode:         leg.ScopeMode,
		Brief:             leg.Brief,
		WorkflowRunID:     runID,
		WorkflowPhase:     phase,
		WorkflowWorkID:    leg.WorkID,
		BlueprintPath:     blueprintPath,
		BlueprintRevision: revision,
		BaseSHA:           baseSHA,
		Files:             leg.Files,
		Criteria:          leg.Criteria,
	}
}

func existingBaseSHA(workers []workerrecords.Worker) string {
	for _, w := range workers {
		if strings.TrimSpace(w.BaseSHA) != "" {
			return strings.TrimSpace(w.BaseSHA)
		}
	}
	return ""
}

// legRunState is a planned leg's state in the plan_from_blueprint result.
func legRunState(rounds []workerrecords.Round) string {
	if len(rounds) == 0 {
		return "planned"
	}
	if last := rounds[len(rounds)-1]; last.SettledAt != "" && last.ResultID != "" {
		return "completed"
	}
	return "started"
}

// RegisterPlanFromBlueprintTool registers plan_from_blueprint on reg.
func RegisterPlanFromBlueprintTool(reg *tools.DefaultRegistry, svc *Service) error {
	const tool = "plan_from_blueprint"
	return reg.Register(tool, func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		sessionID := tctx.Identity.SessionID
		if sessionID == "" {
			return "", fmt.Errorf("%s requires a session identity", tool)
		}
		if agent := tctx.Identity.Agent; tctx.Identity.ParentSessionID != "" || (agent != "" && !strings.EqualFold(agent, "coordinator")) {
			return "", planReject(tool, "coordinator_only", nil)
		}
		blueprintPath, _ := args["blueprint_path"].(string)
		out, err := svc.PlanFromBlueprint(ctx, SeedRequest{
			Actor:         coordinator(sessionID),
			OperationID:   tool + ":" + sessionID + ":" + tctx.Identity.ToolCallID,
			ProjectID:     tctx.Identity.ProjectID,
			WorkspacePath: tctx.ActiveRootPath(),
			BlueprintPath: blueprintPath,
		})
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return "", fmt.Errorf("encode %s output: %w", tool, err)
		}
		return string(raw), nil
	})
}

// PlanWorkers returns the names of the session's open workers planned from
// the Blueprint at blueprintPath.
func (s *Service) PlanWorkers(ctx context.Context, sessionID, blueprintPath string) (map[string]bool, error) {
	workers, err := s.records.ByBlueprint(ctx, coordinator(sessionID), blueprintPath)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(workers))
	for _, w := range workers {
		if w.ClosedAt == "" {
			out[w.Name] = true
		}
	}
	return out, nil
}
