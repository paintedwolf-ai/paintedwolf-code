package workerplan

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// PlanReader loads Blueprint documents by project and path.
type PlanReader interface {
	Get(ctx context.Context, projectID, path string) (*api.Blueprint, error)
}

// ApprovalChecker reports whether a Blueprint revision is currently approved.
type ApprovalChecker interface {
	IsApproved(ctx context.Context, projectID, blueprintPath string) (bool, error)
}

// WorkflowReadyChecker reports whether implement handoff preconditions hold.
type WorkflowReadyChecker interface {
	ImplementWorkflowReady(ctx context.Context, sessionID, blueprintPath string) (bool, error)
}

// RunChecker enforces workflow run runnability.
type RunChecker interface {
	AssertRunnable(ctx context.Context, runID string) error
}

// ActiveRunReader looks up the active workflow run for a coordinator session.
type ActiveRunReader interface {
	ActiveBySession(ctx context.Context, sessionID string) (*api.WorkflowRun, error)
}

// HeadSHAReader captures the current workspace HEAD commit when available.
type HeadSHAReader interface {
	HeadSHA(ctx context.Context, workspacePath string) (string, error)
}

// GitDiffReader checks working-tree and base-commit diffs for file criteria.
type GitDiffReader interface {
	DiffFromRef(ctx context.Context, projectDir, baseSHA, path string) (bool, error)
	HasUncommittedDiff(ctx context.Context, projectDir, path string) (bool, error)
}

// SourceRunReader lists command and verify receipts attributed to a worker session.
type SourceRunReader interface {
	WorkerSourceRuns(ctx context.Context, childSessionID string) ([]workercompletion.WorkerInvocationReceipt, error)
}

// JobStatusReader looks up worker jobs for closeout status checks.
type JobStatusReader interface {
	Get(id string) (*api.WorkerTask, bool)
}

// Service manages Blueprint seeding, round admission gates, criteria evaluation, and plan closeout.
type Service struct {
	Records       *workerrecords.Store
	Plans         PlanReader
	Approvals     ApprovalChecker
	WorkflowReady WorkflowReadyChecker
	Runs          RunChecker
	ActiveRuns    ActiveRunReader
	HeadSHA       HeadSHAReader
	GitDiff       GitDiffReader
	SourceRuns    SourceRunReader
	Inspector     inspector.Inspector
	Security      *scan.SecurityCloseoutChecker
	Jobs          JobStatusReader
	HostDataDir   func(ctx context.Context, projectID string) (string, error)
}

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

// PlanFromBlueprint reconciles planned workers in req.Actor's session from an approved Blueprint.
func (s *Service) PlanFromBlueprint(ctx context.Context, req SeedRequest) (SeedResult, error) {
	if s == nil || s.Records == nil || s.Plans == nil {
		return SeedResult{}, fmt.Errorf("blueprint planning service not configured")
	}
	path := strings.TrimSpace(req.BlueprintPath)
	if path == "" {
		return SeedResult{}, invalidBlueprintArg("blueprint_path_required", "")
	}
	planDoc, err := s.Plans.Get(ctx, req.ProjectID, path)
	if err != nil {
		return SeedResult{}, invalidBlueprintArg("blueprint_not_found", path)
	}
	if err := s.checkBlueprintApproval(ctx, req.Actor.SessionID, req.ProjectID, path, "plan_from_blueprint"); err != nil {
		return SeedResult{}, err
	}
	runID, phase, err := s.checkActiveWorkflowRun(ctx, req.Actor.SessionID, "plan_from_blueprint", path)
	if err != nil {
		return SeedResult{}, err
	}
	legs, err := ParseBlueprintLegs(planDoc)
	if err != nil {
		return SeedResult{}, invalidBlueprintArg(err.Error(), path)
	}
	revision := blueprint.ContentDigest(planDoc.Content)
	existing, err := s.Records.ByBlueprint(ctx, req.Actor, path)
	if err != nil {
		return SeedResult{}, err
	}
	baseSHA := existingBaseSHA(existing)
	if baseSHA == "" && s.HeadSHA != nil && strings.TrimSpace(req.WorkspacePath) != "" {
		if sha, shaErr := s.HeadSHA.HeadSHA(ctx, req.WorkspacePath); shaErr == nil {
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
	sessionWorkers, err := s.Records.Workers(ctx, req.Actor)
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
		rounds, rErr := s.Records.Rounds(ctx, req.Actor, w.ID)
		if rErr != nil {
			return SeedResult{}, rErr
		}
		roundsByWorker[w.ID] = rounds
		if _, wanted := wantByWorkID[w.WorkflowWorkID]; !wanted && len(rounds) == 0 {
			op := workerrecords.Op{ID: fmt.Sprintf("%s:withdraw:%s", req.OperationID, w.ID)}
			if _, cErr := s.Records.Close(ctx, req.Actor, op, w.ID, "withdrawn"); cErr != nil {
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
			updated, uErr := s.Records.UpdatePlanned(ctx, req.Actor, op, cur.ID, spec, nil)
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
		created, cErr := s.Records.CreateWorker(ctx, req.Actor, op, spec)
		if cErr != nil {
			return SeedResult{}, cErr
		}
		takenNames[created.Name] = true
		w, wErr := s.Records.Worker(ctx, req.Actor, created.WorkerID)
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
			updated, uErr := s.Records.UpdatePlanned(ctx, req.Actor, op, w.ID, spec, afterNames)
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
			State:        legRunState(rounds, s.Jobs),
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

func legRunState(rounds []workerrecords.Round, jobs JobStatusReader) string {
	if len(rounds) == 0 {
		return "planned"
	}
	last := rounds[len(rounds)-1]
	if last.SettledAt == "" {
		return "started"
	}
	if jobs != nil {
		if job, ok := jobs.Get(last.JobID); ok && job != nil && job.Status == api.WorkerStatusComplete {
			return "completed"
		}
	}
	if last.ResultID != "" {
		return "completed"
	}
	return "started"
}

func invalidBlueprintArg(reason, blueprintPath string) error {
	data := map[string]any{
		"tool":   "plan_from_blueprint",
		"field":  "blueprint_path",
		"reason": reason,
	}
	if blueprintPath != "" {
		data["blueprint_path"] = blueprintPath
	}
	return &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: data}
}

// RegisterPlanFromBlueprintTool registers plan_from_blueprint on reg.
func RegisterPlanFromBlueprintTool(reg *tools.DefaultRegistry, svc *Service) error {
	if reg == nil || svc == nil {
		return fmt.Errorf("registry and workerplan service required")
	}
	return reg.Register("plan_from_blueprint", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		sessionID := strings.TrimSpace(tctx.Identity.SessionID)
		if sessionID == "" {
			return "", fmt.Errorf("plan_from_blueprint requires a session identity")
		}
		agent := strings.TrimSpace(tctx.Identity.Agent)
		if strings.TrimSpace(tctx.Identity.ParentSessionID) != "" || (agent != "" && !strings.EqualFold(agent, "coordinator")) {
			return "", &toolrejection.ToolReject{
				Code: "TOOL_ARGS_INVALID",
				Data: map[string]any{"tool": "plan_from_blueprint", "reason": "coordinator_only"},
			}
		}
		opID := "plan_from_blueprint:" + sessionID + ":" + strings.TrimSpace(tctx.Identity.ToolCallID)
		blueprintPath, _ := args["blueprint_path"].(string)
		out, err := svc.PlanFromBlueprint(ctx, SeedRequest{
			Actor:         workerrecords.Actor{SessionID: sessionID, Role: workerrecords.RoleCoordinator, PrincipalID: sessionID},
			OperationID:   opID,
			ProjectID:     tctx.Identity.ProjectID,
			WorkspacePath: tctx.ActiveRootPath(),
			BlueprintPath: blueprintPath,
		})
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return "", fmt.Errorf("encode plan_from_blueprint output: %w", err)
		}
		return string(raw), nil
	})
}

// PlanWorkers returns the names of open workers in sessionID bound to plan (exact or suffix/substring match).
func (s *Service) PlanWorkers(ctx context.Context, sessionID, plan string) (map[string]bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	plan = strings.TrimSpace(plan)
	if s == nil || s.Records == nil || sessionID == "" || plan == "" {
		return nil, nil
	}
	actor := workerrecords.Actor{SessionID: sessionID, Role: workerrecords.RoleCoordinator, PrincipalID: sessionID}
	workers, err := s.Records.Workers(ctx, actor)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool)
	for _, w := range workers {
		if w.ClosedAt != "" {
			continue
		}
		if w.BlueprintPath == plan || (w.BlueprintPath != "" && strings.Contains(w.BlueprintPath, plan)) {
			out[w.Name] = true
		}
	}
	return out, nil
}
