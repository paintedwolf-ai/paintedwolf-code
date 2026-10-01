package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hostctx"
	"github.com/lycaon/lycaon/pkg/api"
)

// CatalogResolver hands back the effective catalog whose captured bytes back
// workflow manifests and topologies for a run.
type CatalogResolver func() (*extpacks.EffectiveCatalog, error)

// OrchestratorDeps wires runtime dependencies for OrchestratorImpl.
type OrchestratorDeps struct {
	Delegation PipelineDelegation
	Store      PipelineDelegationStore
	Agents     AgentRegistry
	Models     ModelGroupSelector
	Workflows  WorkflowRunLifecycle
	// Catalog is required for workflow-id runs: the manifest and its topology are
	// provide units, and there is no directory to fall back to.
	Catalog    CatalogResolver
	Workspaces WorkspaceBinder
}

// OrchestratorImpl coordinates multi-agent topologies with iteration enforcement.
type OrchestratorImpl struct {
	delegation   PipelineDelegation
	store        PipelineDelegationStore
	agents       AgentRegistry
	models       ModelGroupSelector
	workflows    WorkflowRunLifecycle
	catalog      CatalogResolver
	workspaces   WorkspaceBinder
	iterationCap *InMemoryIterationCap
	selfTerm     *DefaultSelfTermination

	mu   sync.RWMutex
	runs map[string]*runState
}

type runState struct {
	runID string
	// delegationID is bound under o.mu once setup completes.
	delegationID    string
	workflowRunID   string
	workflowID      string
	workflowVersion string
	blueprintPath   string
	// Topology maps belong to the runner; parallel stages only read a settled batch.
	completed   map[string]bool
	outputs     map[string]string
	stageLegs   map[string]string
	stagePhases map[string]map[string]bool
	// Lifecycle fields below are shared with Status and Cancel under OrchestratorImpl.mu.
	phase           string
	active          bool
	completedAt     time.Time
	cancelReason    TerminationReason
	cancelRequested bool
}

// runRetentionWindow keeps final status available after return.
const runRetentionWindow = 10 * time.Minute

// registerRun stores a new run and opportunistically drops runs that finished
// more than runRetentionWindow ago, keeping o.runs bounded without a
// background sweeper.
func (o *OrchestratorImpl) registerRun(runID string, state *runState) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	for id, st := range o.runs {
		if !st.active && !st.completedAt.IsZero() && now.Sub(st.completedAt) > runRetentionWindow {
			delete(o.runs, id)
		}
	}
	o.runs[runID] = state
}

// NewOrchestratorImpl constructs an orchestrator with the given dependencies.
func NewOrchestratorImpl(deps OrchestratorDeps) *OrchestratorImpl {
	return &OrchestratorImpl{
		delegation:   deps.Delegation,
		store:        deps.Store,
		agents:       deps.Agents,
		models:       deps.Models,
		workflows:    deps.Workflows,
		catalog:      deps.Catalog,
		workspaces:   deps.Workspaces,
		iterationCap: NewInMemoryIterationCap(MaxIterations),
		selfTerm:     NewDefaultSelfTermination(),
		runs:         make(map[string]*runState),
	}
}

// LoadTopology parses YAML and validates agent profiles via the registry.
func (o *OrchestratorImpl) LoadTopology(ctx context.Context, path extpacks.Source) (*TopologySpec, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	spec, err := LoadTopologyFromFile(path)
	if err != nil {
		return nil, err
	}
	if err := o.validateTopologyProfiles(spec); err != nil {
		return nil, err
	}
	return spec, nil
}

// LoadTopologyForID resolves a topology from the catalog's captured bytes and
// validates its agent profiles.
func (o *OrchestratorImpl) LoadTopologyForID(ctx context.Context, catalog *extpacks.EffectiveCatalog, topologyID string) (*TopologySpec, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	spec, err := TopologySpecForID(catalog, topologyID)
	if err != nil {
		return nil, err
	}
	if err := o.validateTopologyProfiles(spec); err != nil {
		return nil, err
	}
	return spec, nil
}

func (o *OrchestratorImpl) validateTopologyProfiles(spec *TopologySpec) error {
	if o == nil || o.agents == nil {
		return fmt.Errorf("agent registry not configured")
	}
	if spec.Pipeline != nil {
		for _, stage := range spec.Pipeline.Stages {
			if _, err := o.agents.Get(stage.AgentProfile); err != nil {
				return fmt.Errorf("pipeline stage %q profile %q: %w", stage.Name, stage.AgentProfile, err)
			}
		}
	}
	if spec.Supervisor != nil {
		if err := validateSupervisorSpec(*spec.Supervisor); err != nil {
			return err
		}
		for _, id := range spec.Supervisor.ProfileIDs {
			if _, err := o.agents.Get(id); err != nil {
				return fmt.Errorf("supervisor profile %q: %w", id, err)
			}
		}
	}
	if spec.Pack != nil {
		if err := validatePackSpec(*spec.Pack); err != nil {
			return fmt.Errorf("pack: %w", err)
		}
		if _, err := o.agents.Get(effectivePackProfile(*spec.Pack)); err != nil {
			return fmt.Errorf("pack profile %q: %w", effectivePackProfile(*spec.Pack), err)
		}
	}
	if spec.FanOut != nil {
		if err := validateFanOutSpec(*spec.FanOut); err != nil {
			return fmt.Errorf("fan_out: %w", err)
		}
		if _, err := o.agents.Get(effectiveFanOutProfile(*spec.FanOut)); err != nil {
			return fmt.Errorf("fan_out profile %q: %w", effectiveFanOutProfile(*spec.FanOut), err)
		}
	}
	return nil
}

// Run executes a topology run.
func (o *OrchestratorImpl) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	if o == nil {
		return nil, fmt.Errorf("orchestrator not configured")
	}
	resolved, wfRun, err := o.resolveRunRequest(ctx, req)
	if err != nil {
		return nil, o.settleRunFailure(ctx, workflowContextFromInput(req), "", err)
	}
	var result *RunResult
	switch resolved.Topology.Pattern {
	case TopologyPipeline:
		result, err = o.runPipeline(ctx, resolved, wfRun)
	case TopologySupervisor:
		result, err = o.runSupervisor(ctx, resolved)
	case TopologyFanOut:
		result, err = o.runFanOut(ctx, resolved, wfRun)
	case TopologyPack:
		result, err = o.runPack(ctx, resolved, wfRun)
	default:
		err = fmt.Errorf("topology pattern %q not implemented (supported: %v)", resolved.Topology.Pattern, allTopologyPatterns)
	}
	if err != nil {
		phase := ""
		if wfRun != nil {
			phase = o.workflowPhase(ctx, wfRun.runID)
		}
		return result, o.settleRunFailure(ctx, wfRun, phase, err)
	}
	return result, nil
}

func (o *OrchestratorImpl) settleRunFailure(ctx context.Context, wf *workflowRunContext, phase string, runErr error) error {
	if runErr == nil || wf == nil || o.workflows == nil || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		return runErr
	}
	if _, err := o.workflows.Fail(ctx, wf.runID, workflowFailureFor(runErr, phase)); err != nil {
		return errors.Join(runErr, fmt.Errorf("settle workflow failure: %w", err))
	}
	return runErr
}

func (o *OrchestratorImpl) workflowPhase(ctx context.Context, runID string) string {
	if o == nil || o.workflows == nil || strings.TrimSpace(runID) == "" {
		return ""
	}
	run, err := o.workflows.Get(ctx, runID)
	if err != nil || run == nil {
		return ""
	}
	return run.CurrentPhase
}

type workflowRunContext struct {
	runID         string
	id            string
	version       string
	blueprintPath string
	stagePhases   map[string]map[string]bool
}

func (o *OrchestratorImpl) resolveRunRequest(ctx context.Context, req RunRequest) (RunRequest, *workflowRunContext, error) {
	workflowID := strings.TrimSpace(req.WorkflowID)
	if workflowID == "" {
		if req.Topology.Pattern == "" {
			return RunRequest{}, nil, fmt.Errorf("topology or workflow_id required")
		}
		if wf := workflowContextFromInput(req); wf != nil {
			return req, wf, nil
		}
		return req, nil, nil
	}
	if o.catalog == nil {
		return RunRequest{}, nil, fmt.Errorf("effective catalog resolver not configured")
	}
	catalog, err := o.catalog()
	if err != nil {
		return RunRequest{}, nil, err
	}
	version := strings.TrimSpace(req.WorkflowVersion)
	if version == "" {
		version = "1.0.0"
	}
	manifest, err := LoadWorkflowManifest(catalog, workflowID, version)
	if err != nil {
		return RunRequest{}, nil, err
	}
	spec, err := o.LoadTopologyForID(ctx, catalog, manifest.TopologyID)
	if err != nil {
		return RunRequest{}, nil, err
	}
	req.Topology = *spec
	req.WorkflowID = manifest.ID
	req.WorkflowVersion = manifest.Version

	var wfCtx *workflowRunContext
	if existing := workflowContextFromInput(req); existing != nil {
		wfCtx = existing
		if wfCtx.id == "" {
			wfCtx.id = manifest.ID
		}
		if wfCtx.version == "" {
			wfCtx.version = manifest.Version
		}
		wfCtx.stagePhases = manifest.StagePhases
	} else if o.workflows != nil {
		sessionID := strings.TrimSpace(req.SessionID)
		if sessionID == "" {
			return RunRequest{}, nil, fmt.Errorf("session_id required for workflow run")
		}
		run, err := o.workflows.Start(hostctx.WithHumanWorkflowStart(ctx), sessionID, api.StartWorkflowRunRequest{
			WorkflowID:      manifest.ID,
			WorkflowVersion: manifest.Version,
		})
		if err != nil {
			return RunRequest{}, nil, err
		}
		wfCtx = &workflowRunContext{
			runID:         run.ID,
			id:            manifest.ID,
			version:       manifest.Version,
			blueprintPath: run.BlueprintPath,
			stagePhases:   manifest.StagePhases,
		}
	}
	return req, wfCtx, nil
}

func workflowContextFromInput(req RunRequest) *workflowRunContext {
	if req.Input == nil {
		return nil
	}
	runID, _ := req.Input["workflow_run_id"].(string)
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	wfID, _ := req.Input["workflow_id"].(string)
	wfVer, _ := req.Input["workflow_version"].(string)
	blueprintPath, _ := req.Input["blueprint_path"].(string)
	return &workflowRunContext{
		runID:         runID,
		id:            strings.TrimSpace(wfID),
		version:       strings.TrimSpace(wfVer),
		blueprintPath: strings.TrimSpace(blueprintPath),
	}
}

// Status reports in-progress orchestration state.
func (o *OrchestratorImpl) Status(ctx context.Context, runID string) (*RunStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	o.mu.RLock()
	st, ok := o.runs[runID]
	var phase string
	var active bool
	var workflowRunID string
	if ok {
		// Copy fields out while holding the lock: Cancel and the completion
		// paths mutate phase/active under o.mu.Lock(), so reading them after
		// releasing RLock would race.
		phase = st.phase
		active = st.active
		workflowRunID = st.workflowRunID
	}
	o.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("run %q not found", runID)
	}
	if o.workflows != nil && workflowRunID != "" {
		if run, err := o.workflows.Get(ctx, workflowRunID); err == nil && run != nil {
			workflowPhase, terminal := workflowStatusPhase(run)
			if workflowPhase != "" {
				phase = workflowPhase
			}
			if terminal {
				active = false
			}
		}
	}
	return &RunStatus{
		RunID:  runID,
		Phase:  phase,
		Active: active,
	}, nil
}

func (o *OrchestratorImpl) runPipeline(ctx context.Context, req RunRequest, wf *workflowRunContext) (*RunResult, error) {
	if o.delegation == nil || o.store == nil || o.agents == nil {
		return nil, fmt.Errorf("orchestrator pipeline dependencies not configured")
	}
	if req.Topology.Pipeline == nil {
		return nil, fmt.Errorf("topology missing pipeline spec")
	}
	stages := req.Topology.Pipeline.Stages
	if err := validatePipelineStages(stages); err != nil {
		return nil, err
	}
	for _, stage := range stages {
		if _, err := o.agents.Get(stage.AgentProfile); err != nil {
			return nil, fmt.Errorf("pipeline stage %q profile %q: %w", stage.Name, stage.AgentProfile, err)
		}
	}

	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session_id required for pipeline run")
	}
	projectDir, err := pipelineProjectDir(req)
	if err != nil {
		return nil, err
	}

	runID := uuid.NewString()
	state := &runState{
		runID:     runID,
		completed: make(map[string]bool, len(stages)),
		outputs:   make(map[string]string, len(stages)),
		stageLegs: make(map[string]string, len(stages)),
		phase:     "pipeline",
		active:    true,
	}
	if wf != nil {
		state.workflowRunID = wf.runID
		state.workflowID = wf.id
		state.workflowVersion = wf.version
		state.blueprintPath = wf.blueprintPath
		state.stagePhases = wf.stagePhases
		state.phase = "research"
	} else if inline := workflowContextFromInput(req); inline != nil {
		state.workflowRunID = inline.runID
		state.workflowID = inline.id
		state.workflowVersion = inline.version
		if state.workflowID != "" {
			state.phase = "research"
		}
	}
	o.registerRun(runID, state)
	defer func() {
		o.mu.Lock()
		state.active = false
		state.completedAt = time.Now()
		if state.workflowRunID == "" && state.phase == "pipeline" {
			state.phase = "done"
		}
		o.mu.Unlock()
	}()

	delegationID, err := o.setupPipelineDelegation(ctx, req, sessionID, projectDir, stages, state)
	if err != nil {
		return nil, err
	}
	if err := o.bindRunDelegation(ctx, state, delegationID); err != nil {
		return nil, err
	}
	for _, stage := range stages {
		if !state.completed[stage.Name] || o.workflows == nil || state.workflowRunID == "" {
			continue
		}
		if err := o.workflows.MarkTopologyStageComplete(ctx, state.workflowRunID, stage.Name, state.outputs[stage.Name], ""); err != nil {
			return nil, err
		}
	}

	for len(state.completed) < len(stages) {
		ready := readyPipelineStages(stages, state.completed)
		if len(ready) == 0 {
			return nil, newRunFailure(RunFailureCodePipelineStalled, "", false, fmt.Errorf("pipeline stalled with no ready stages"))
		}
		results, err := o.executePipelineWave(ctx, req, runID, delegationID, ready, state)
		if err != nil {
			return nil, err
		}
		for _, result := range results {
			state.outputs[result.stage] = result.output
			state.completed[result.stage] = true
			if o.workflows != nil && state.workflowRunID != "" {
				if err := o.workflows.MarkTopologyStageComplete(ctx, state.workflowRunID, result.stage, result.output, ""); err != nil {
					return nil, err
				}
			}
		}
		o.refreshRunPhase(ctx, state)
	}

	finalOutput := state.outputs[stages[len(stages)-1].Name]
	return &RunResult{
		RunID:        runID,
		FinalOutput:  finalOutput,
		StageOutputs: copyStringMap(state.outputs),
	}, nil
}

func pipelineProjectDir(req RunRequest) (string, error) {
	if req.Input != nil {
		if v, ok := req.Input["project_dir"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), nil
		}
	}
	return "", fmt.Errorf("project_dir required in RunRequest.Input")
}

func pipelineProjectID(req RunRequest) (string, error) {
	if req.Input != nil {
		if v, ok := req.Input["project_id"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), nil
		}
	}
	return "", fmt.Errorf("project_id required in RunRequest.Input")
}

func (o *OrchestratorImpl) setupPipelineDelegation(
	ctx context.Context,
	req RunRequest,
	sessionID, projectDir string,
	stages []PipelineStage,
	state *runState,
) (string, error) {
	if req.Input != nil {
		if v, ok := req.Input["delegation_id"].(string); ok && strings.TrimSpace(v) != "" {
			delegationID := strings.TrimSpace(v)
			legs, err := o.store.ListLegs(ctx, delegationID)
			if err != nil {
				return "", err
			}
			restorePipelineLegs(state, legs)
			return delegationID, nil
		}
	}
	if strings.TrimSpace(state.workflowRunID) != "" {
		delegationID, ok, err := o.store.DelegationByWorkflowRunID(ctx, state.workflowRunID)
		if err != nil {
			return "", err
		}
		if ok {
			legs, listErr := o.store.ListLegs(ctx, delegationID)
			if listErr != nil {
				return "", listErr
			}
			restorePipelineLegs(state, legs)
			if len(state.stageLegs) != len(stages) {
				return "", fmt.Errorf("existing delegation has %d legs, pipeline needs %d", len(state.stageLegs), len(stages))
			}
			return delegationID, nil
		}
	}
	if strings.TrimSpace(state.workflowRunID) == "" {
		if delegationID, ok := o.store.DelegationBySessionID(sessionID); ok {
			legs, err := o.store.ListLegs(ctx, delegationID)
			if err != nil {
				return "", err
			}
			restorePipelineLegs(state, legs)
			if len(state.stageLegs) != len(stages) {
				return "", fmt.Errorf("existing delegation has %d legs, pipeline needs %d", len(state.stageLegs), len(stages))
			}
			return delegationID, nil
		}
	}

	task := strings.TrimSpace(req.Topology.Task)
	if task == "" {
		task = "pipeline run"
	}
	projectID, err := pipelineProjectID(req)
	if err != nil {
		return "", err
	}
	delegation := api.Delegation{
		ProjectID:       projectID,
		WorkspacePath:   projectDir,
		Task:            task,
		Strategy:        api.HuntStrategyFileBased,
		Status:          "active",
		Phase:           api.DelegationPhaseSetup,
		WorkflowID:      state.workflowID,
		WorkflowVersion: state.workflowVersion,
		WorkflowRunID:   state.workflowRunID,
	}
	legs := make([]api.Leg, len(stages))
	for i := range stages {
		leg := buildPipelineLeg(stages[i], req.Topology, nil, state.blueprintPath)
		legs[i] = leg
		state.stageLegs[stages[i].Name] = leg.ID
	}
	created, err := o.store.Create(ctx, delegation, sessionID, legs)
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

func restorePipelineLegs(state *runState, legs []api.Leg) {
	for _, leg := range legs {
		state.stageLegs[leg.Title] = leg.ID
		if leg.Status != api.LegStatusComplete {
			continue
		}
		output := leg.Title + " complete"
		if leg.Result != nil && strings.TrimSpace(leg.Result.Summary) != "" {
			output = leg.Result.Summary
		}
		state.completed[leg.Title] = true
		state.outputs[leg.Title] = output
	}
}

func buildPipelineLeg(stage PipelineStage, spec TopologySpec, outputs map[string]string, blueprintPath string) api.Leg {
	return api.Leg{
		ID:        uuid.NewString(),
		Title:     stage.Name,
		AgentType: stage.AgentProfile,
		Prompt:    buildStagePrompt(spec, stage, outputs, blueprintPath),
		Files:     []string{wholeProjectScopeGlob},
		Status:    api.LegStatusPending,
		CreatedAt: time.Now().UTC(),
	}
}

func buildStagePrompt(spec TopologySpec, stage PipelineStage, outputs map[string]string, blueprintPath string) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("Stage: %s", stage.Name))
	if task := strings.TrimSpace(spec.Task); task != "" {
		parts = append(parts, task)
	}
	if path := strings.TrimSpace(blueprintPath); path != "" {
		parts = append(parts, fmt.Sprintf("Bound blueprint: %s", path))
	}
	for _, pred := range stage.InputFrom {
		if out, ok := outputs[pred]; ok && strings.TrimSpace(out) != "" {
			parts = append(parts, fmt.Sprintf("Output from %s:\n%s", pred, out))
		}
	}
	return strings.Join(parts, "\n\n")
}

type pipelineStageResult struct {
	stage  string
	output string
}

func (o *OrchestratorImpl) executePipelineWave(
	ctx context.Context,
	req RunRequest,
	runID, delegationID string,
	stages []PipelineStage,
	state *runState,
) ([]pipelineStageResult, error) {
	waveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan struct {
		result pipelineStageResult
		err    error
	}, len(stages))
	for _, stage := range stages {
		go func() {
			result, err := o.executePipelineStage(waveCtx, req, runID, delegationID, stage, state)
			results <- struct {
				result pipelineStageResult
				err    error
			}{result: result, err: err}
		}()
	}
	out := make([]pipelineStageResult, 0, len(stages))
	var firstErr error
	for range stages {
		settled := <-results
		if settled.err != nil {
			if firstErr == nil || errors.Is(firstErr, context.Canceled) {
				firstErr = settled.err
				cancel()
			}
			continue
		}
		out = append(out, settled.result)
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func (o *OrchestratorImpl) executePipelineStage(
	ctx context.Context,
	req RunRequest,
	runID, delegationID string,
	stage PipelineStage,
	state *runState,
) (pipelineStageResult, error) {
	if err := o.waitForWorkflowStagePhase(ctx, state, stage.Name); err != nil {
		return pipelineStageResult{}, err
	}
	legID, ok := state.stageLegs[stage.Name]
	if !ok {
		return pipelineStageResult{}, newRunFailure(RunFailureCodeStageMissing, stage.Name, false, fmt.Errorf("pipeline stage %q missing leg", stage.Name))
	}
	if len(stage.InputFrom) > 0 {
		leg, err := o.store.GetLeg(ctx, delegationID, legID)
		if err != nil {
			return pipelineStageResult{}, err
		}
		if leg.Status == api.LegStatusPending {
			leg.Prompt = buildStagePrompt(req.Topology, stage, state.outputs, state.blueprintPath)
			if err := o.store.UpdateLeg(ctx, *leg); err != nil {
				return pipelineStageResult{}, err
			}
		}
	}
	for {
		leg, err := o.store.GetLeg(ctx, delegationID, legID)
		if err != nil {
			return pipelineStageResult{}, err
		}
		switch leg.Status {
		case api.LegStatusPending:
			if err := o.beforePipelineDispatch(ctx, req, runID, stage, state); err != nil {
				return pipelineStageResult{}, err
			}
			if _, err := o.delegation.DispatchLeg(ctx, delegationID, legID, ""); err != nil {
				return pipelineStageResult{}, err
			}
		case api.LegStatusRetryPending:
			if err := o.beforePipelineDispatch(ctx, req, runID, stage, state); err != nil {
				return pipelineStageResult{}, err
			}
			resumer, ok := o.delegation.(pipelineLegResumer)
			if !ok {
				return pipelineStageResult{}, newRunFailure(RunFailureCodeRetryUnavailable, stage.Name, false, fmt.Errorf("pipeline delegation cannot resume a leg"))
			}
			if _, err := resumer.ResumeLeg(ctx, delegationID, legID); err != nil {
				return pipelineStageResult{}, newRunFailure(RunFailureCodeRetryExhausted, stage.Name, false, err)
			}
		case api.LegStatusComplete:
			output := stage.Name + " complete"
			if leg.Result != nil && strings.TrimSpace(leg.Result.Summary) != "" {
				output = leg.Result.Summary
			}
			return pipelineStageResult{stage: stage.Name, output: output}, nil
		case api.LegStatusFailed, api.LegStatusCanceled:
			return pipelineStageResult{}, newRunFailure(RunFailureCodeStageFailed, stage.Name, false, fmt.Errorf("pipeline stage %q leg %s", stage.Name, leg.Status))
		case api.LegStatusDispatched, api.LegStatusRunning, api.LegStatusHeld:
		}
		if _, err := o.waitForLeg(ctx, delegationID, legID); err != nil {
			return pipelineStageResult{}, err
		}
	}
}

func (o *OrchestratorImpl) beforePipelineDispatch(ctx context.Context, req RunRequest, runID string, stage PipelineStage, state *runState) error {
	taskID := runID + ":" + stage.Name
	if _, err := o.iterationCap.Track(ctx, stage.AgentProfile, taskID); err != nil {
		return err
	}
	if err := o.checkIteration(ctx, req.Topology, stage.AgentProfile, taskID, TerminationContext{AgentID: stage.AgentProfile, TaskID: taskID}); err != nil {
		return err
	}
	if o.workflows != nil && state.workflowRunID != "" {
		return o.workflows.AssertRunnable(ctx, state.workflowRunID)
	}
	return nil
}

func (o *OrchestratorImpl) refreshRunPhase(ctx context.Context, state *runState) {
	if o == nil || o.workflows == nil || state == nil || state.workflowRunID == "" {
		return
	}
	run, err := o.workflows.Get(ctx, state.workflowRunID)
	if err != nil || run == nil || strings.TrimSpace(run.CurrentPhase) == "" {
		return
	}
	o.mu.Lock()
	if !state.cancelRequested {
		state.phase = run.CurrentPhase
	}
	o.mu.Unlock()
}

func (o *OrchestratorImpl) waitForWorkflowStagePhase(ctx context.Context, state *runState, stage string) error {
	if o == nil || o.workflows == nil || state == nil || strings.TrimSpace(state.workflowRunID) == "" {
		return nil
	}
	allowed := state.stagePhases[strings.TrimSpace(stage)]
	if len(allowed) == 0 {
		return nil
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := o.workflows.AssertRunnable(ctx, state.workflowRunID); err != nil {
			return err
		}
		run, err := o.workflows.Get(ctx, state.workflowRunID)
		if err != nil {
			return err
		}
		phase := strings.TrimSpace(run.CurrentPhase)
		o.mu.Lock()
		if !state.cancelRequested {
			state.phase = phase
		}
		o.mu.Unlock()
		if allowed[phase] {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// settleLegs waits until every leg is complete or failed. A leg that ran out
// of budget resumes with a larger one. Legs are polled together so one leg's
// retry never waits behind another's work; callers decide what a failed leg means.
func (o *OrchestratorImpl) settleLegs(ctx context.Context, delegationID string, legIDs []string, stage string) ([]*api.Leg, error) {
	settled := make([]*api.Leg, len(legIDs))
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		open := 0
		for i, legID := range legIDs {
			if settled[i] != nil {
				continue
			}
			leg, err := o.store.GetLeg(ctx, delegationID, legID)
			if err != nil {
				return nil, err
			}
			switch leg.Status {
			case api.LegStatusComplete, api.LegStatusFailed, api.LegStatusCanceled:
				settled[i] = leg
				continue
			case api.LegStatusRetryPending:
				resumer, ok := o.delegation.(pipelineLegResumer)
				if !ok {
					return nil, newRunFailure(RunFailureCodeRetryUnavailable, stage, false, fmt.Errorf("%s delegation cannot resume a leg", stage))
				}
				if _, err := resumer.ResumeLeg(ctx, delegationID, legID); err != nil {
					return nil, newRunFailure(RunFailureCodeRetryExhausted, stage, false, err)
				}
			case api.LegStatusPending, api.LegStatusDispatched, api.LegStatusRunning, api.LegStatusHeld:
			}
			open++
		}
		if open == 0 {
			return settled, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (o *OrchestratorImpl) waitForLeg(ctx context.Context, delegationID, legID string) (*api.Leg, error) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		leg, err := o.store.GetLeg(ctx, delegationID, legID)
		if err != nil {
			return nil, err
		}
		if isSettledLegAttempt(leg.Status) {
			return leg, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func isSettledLegAttempt(status api.LegStatus) bool {
	return status.IsTerminal() || status == api.LegStatusRetryPending
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// checkIteration enforces iteration cap and self-termination before re-dispatch.
func (o *OrchestratorImpl) checkIteration(ctx context.Context, spec TopologySpec, agentID, taskID string, tc TerminationContext) error {
	if o == nil {
		return fmt.Errorf("orchestrator not configured")
	}
	max := EffectiveIterationCap(spec)
	if dec, err := o.iterationCap.CheckMax(ctx, agentID, taskID, max); err != nil {
		return err
	} else if dec != nil && dec.ShouldTerminate {
		return fmt.Errorf("iteration terminated: %s", dec.Reason)
	}
	if dec, err := o.selfTerm.Evaluate(ctx, tc); err != nil {
		return err
	} else if dec != nil && dec.ShouldTerminate {
		return fmt.Errorf("self-termination: %s", dec.Reason)
	}
	return nil
}
