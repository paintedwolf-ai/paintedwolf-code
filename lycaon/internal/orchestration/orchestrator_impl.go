package orchestration

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hostctx"
	"github.com/lycaon/lycaon/pkg/api"
)

// CatalogResolver returns the effective catalog whose captured bytes back workflow manifests and topologies.
type CatalogResolver func() (*extpacks.EffectiveCatalog, error)

// OrchestratorDeps wires runtime dependencies for OrchestratorImpl.
type OrchestratorDeps struct {
	Workers    TopologyWorkers
	Agents     AgentRegistry
	Workflows  *WorkflowRunLifecycle
	Catalog    CatalogResolver
	Workspaces WorkspaceBinder
}

// OrchestratorImpl coordinates multi-agent topologies over worker batches.
type OrchestratorImpl struct {
	workers    TopologyWorkers
	agents     AgentRegistry
	workflows  *WorkflowRunLifecycle
	catalog    CatalogResolver
	workspaces WorkspaceBinder

	mu   sync.RWMutex
	runs map[string]*runState
}

type runState struct {
	runID           string
	batchID         string
	workflowRunID   string
	workflowID      string
	workflowVersion string
	blueprintPath   string
	completed       map[string]bool
	outputs         map[string]string
	stageUnits      map[string]string
	stagePhases     map[string]map[string]bool
	phase           string
	active          bool
	completedAt     time.Time
	cancelReason    TerminationReason
	cancelRequested bool
}

const runRetentionWindow = 10 * time.Minute

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
		workers:    deps.Workers,
		agents:     deps.Agents,
		workflows:  deps.Workflows,
		catalog:    deps.Catalog,
		workspaces: deps.Workspaces,
		runs:       make(map[string]*runState),
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

// LoadTopologyForID resolves a topology from the catalog's captured bytes and validates its profiles.
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
	if _, err := o.workflows.Controls.Fail(ctx, wf.runID, workflowFailureFor(runErr, phase)); err != nil {
		return errors.Join(runErr, fmt.Errorf("settle workflow failure: %w", err))
	}
	return runErr
}

func (o *OrchestratorImpl) workflowPhase(ctx context.Context, runID string) string {
	if o == nil || o.workflows == nil || strings.TrimSpace(runID) == "" {
		return ""
	}
	run, err := o.workflows.Runs.Get(ctx, runID)
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
		run, err := o.workflows.Starts.Start(hostctx.WithHumanWorkflowStart(ctx), sessionID, api.StartWorkflowRunRequest{
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
		phase = st.phase
		active = st.active
		workflowRunID = st.workflowRunID
	}
	o.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("run %q not found", runID)
	}
	if o.workflows != nil && workflowRunID != "" {
		if run, err := o.workflows.Runs.Get(ctx, workflowRunID); err == nil && run != nil {
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
	if o.workers == nil || o.agents == nil {
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
	state := newPipelineRunState(runID, len(stages), req, wf)
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

	batchID, err := o.setupPipelineBatch(ctx, req, sessionID, projectDir, stages, state)
	if err != nil {
		return nil, err
	}
	if err := o.bindRunBatch(ctx, state, batchID); err != nil {
		return nil, err
	}
	if err := o.markCompletedPipelineStages(ctx, stages, state); err != nil {
		return nil, err
	}

	for len(state.completed) < len(stages) {
		ready := readyPipelineStages(stages, state.completed)
		if len(ready) == 0 {
			return nil, newRunFailure(RunFailureCodePipelineStalled, "", fmt.Errorf("pipeline stalled with no ready stages"))
		}
		results, err := o.executePipelineWave(ctx, req, batchID, ready, state)
		if err != nil {
			return nil, err
		}
		for _, result := range results {
			state.outputs[result.stage] = result.output
			state.completed[result.stage] = true
			if o.workflows != nil && state.workflowRunID != "" {
				if err := o.workflows.Topology.MarkTopologyStageComplete(ctx, state.workflowRunID, result.stage, result.output, ""); err != nil {
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

func newPipelineRunState(runID string, stageCount int, req RunRequest, wf *workflowRunContext) *runState {
	state := &runState{
		runID:      runID,
		completed:  make(map[string]bool, stageCount),
		outputs:    make(map[string]string, stageCount),
		stageUnits: make(map[string]string, stageCount),
		phase:      "pipeline",
		active:     true,
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
	return state
}

func (o *OrchestratorImpl) markCompletedPipelineStages(ctx context.Context, stages []PipelineStage, state *runState) error {
	if o.workflows == nil || state.workflowRunID == "" {
		return nil
	}
	for _, stage := range stages {
		if !state.completed[stage.Name] {
			continue
		}
		if err := o.workflows.Topology.MarkTopologyStageComplete(ctx, state.workflowRunID, stage.Name, state.outputs[stage.Name], ""); err != nil {
			return err
		}
	}
	return nil
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

func boundPhaseForStage(state *runState, stage string) string {
	if state == nil || len(state.stagePhases) == 0 {
		return ""
	}
	phases := state.stagePhases[strings.TrimSpace(stage)]
	if len(phases) == 0 {
		return ""
	}
	names := make([]string, 0, len(phases))
	for p, ok := range phases {
		if ok && strings.TrimSpace(p) != "" {
			names = append(names, strings.TrimSpace(p))
		}
	}
	slices.Sort(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func (o *OrchestratorImpl) setupPipelineBatch(
	ctx context.Context,
	req RunRequest,
	sessionID, projectDir string,
	stages []PipelineStage,
	state *runState,
) (string, error) {
	batchID, _ := req.Input["batch_id"].(string)
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		if state.workflowRunID != "" {
			var err error
			batchID, _, err = o.workers.BatchByWorkflowRun(ctx, state.workflowRunID)
			if err != nil {
				return "", err
			}
		} else {
			var err error
			batchID, _, err = o.workers.BatchBySession(ctx, sessionID)
			if err != nil {
				return "", err
			}
		}
	}
	if batchID != "" {
		keys := make([]string, len(stages))
		for i, stage := range stages {
			keys[i] = stage.Name
		}
		restored, err := o.restoreBatch(ctx, batchID, keys, state)
		if err != nil {
			return "", err
		}
		return restored.ID, nil
	}

	task := strings.TrimSpace(req.Topology.Task)
	if task == "" {
		task = "pipeline run"
	}
	projectID, err := pipelineProjectID(req)
	if err != nil {
		return "", err
	}
	batch := TopologyBatch{
		SessionID:       sessionID,
		ProjectID:       projectID,
		WorkspacePath:   projectDir,
		Task:            task,
		Pattern:         TopologyPipeline,
		Status:          TopologyBatchActive,
		WorkflowID:      state.workflowID,
		WorkflowVersion: state.workflowVersion,
		WorkflowRunID:   state.workflowRunID,
		BlueprintPath:   state.blueprintPath,
	}
	units := make([]TopologyUnit, len(stages))
	for i := range stages {
		u := buildPipelineUnit(stages[i], req.Topology, nil, state)
		units[i] = u
	}
	created, createdUnits, err := o.workers.CreateBatch(ctx, batch, units)
	if err != nil {
		return "", err
	}
	for i := range stages {
		state.stageUnits[stages[i].Name] = createdUnits[i].ID
	}
	return created.ID, nil
}

func buildPipelineUnit(stage PipelineStage, spec TopologySpec, outputs map[string]string, state *runState) TopologyUnit {
	var blueprintPath string
	if state != nil {
		blueprintPath = state.blueprintPath
	}
	return TopologyUnit{
		ID:        uuid.NewString(),
		WorkID:    stage.Name,
		AgentType: stage.AgentProfile,
		Prompt:    buildStagePrompt(spec, stage, outputs, blueprintPath),
		Phase:     boundPhaseForStage(state, stage.Name),
		Status:    api.LegStatusPending,
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
	batchID string,
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
		go func(st PipelineStage) {
			result, err := o.executePipelineStage(waveCtx, req, batchID, st, state)
			results <- struct {
				result pipelineStageResult
				err    error
			}{result: result, err: err}
		}(stage)
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
	batchID string,
	stage PipelineStage,
	state *runState,
) (pipelineStageResult, error) {
	if err := o.waitForWorkflowStagePhase(ctx, state, stage.Name); err != nil {
		return pipelineStageResult{}, err
	}
	unitID, ok := state.stageUnits[stage.Name]
	if !ok {
		return pipelineStageResult{}, newRunFailure(RunFailureCodeStageMissing, stage.Name, fmt.Errorf("pipeline stage %q missing unit", stage.Name))
	}
	if len(stage.InputFrom) > 0 {
		unit, err := o.workers.GetUnit(ctx, batchID, unitID)
		if err != nil {
			return pipelineStageResult{}, err
		}
		if unit.Status == api.LegStatusPending {
			unit.Prompt = buildStagePrompt(req.Topology, stage, state.outputs, state.blueprintPath)
			if err := o.workers.UpdateUnit(ctx, batchID, *unit); err != nil {
				return pipelineStageResult{}, err
			}
		}
	}
	for {
		unit, err := o.workers.GetUnit(ctx, batchID, unitID)
		if err != nil {
			return pipelineStageResult{}, err
		}
		switch unit.Status {
		case api.LegStatusPending:
			if err := o.assertWorkflowRunnable(ctx, state.workflowRunID); err != nil {
				return pipelineStageResult{}, err
			}
			if _, err := o.workers.DispatchUnit(ctx, batchID, unitID); err != nil {
				return pipelineStageResult{}, err
			}
		case api.LegStatusComplete:
			output := stage.Name + " complete"
			if strings.TrimSpace(unit.Summary) != "" {
				output = unit.Summary
			}
			return pipelineStageResult{stage: stage.Name, output: output}, nil
		case api.LegStatusFailed, api.LegStatusCanceled:
			return pipelineStageResult{}, newRunFailure(RunFailureCodeStageFailed, stage.Name, fmt.Errorf("pipeline stage %q %s", stage.Name, unit.Status))
		case api.LegStatusDispatched, api.LegStatusRunning, api.LegStatusHeld, api.LegStatusRetryPending:
		}
		if _, err := o.waitForUnit(ctx, batchID, unitID); err != nil {
			return pipelineStageResult{}, err
		}
	}
}

func (o *OrchestratorImpl) assertWorkflowRunnable(ctx context.Context, workflowRunID string) error {
	if o.workflows != nil && strings.TrimSpace(workflowRunID) != "" {
		return o.workflows.Policy.AssertRunnable(ctx, workflowRunID)
	}
	return nil
}

func (o *OrchestratorImpl) refreshRunPhase(ctx context.Context, state *runState) {
	if o == nil || o.workflows == nil || state == nil || state.workflowRunID == "" {
		return
	}
	run, err := o.workflows.Runs.Get(ctx, state.workflowRunID)
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
		if err := o.workflows.Policy.AssertRunnable(ctx, state.workflowRunID); err != nil {
			return err
		}
		run, err := o.workflows.Runs.Get(ctx, state.workflowRunID)
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

func (o *OrchestratorImpl) settleUnits(ctx context.Context, batchID string, unitIDs []string) ([]*TopologyUnit, error) {
	settled := make([]*TopologyUnit, len(unitIDs))
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		open := 0
		for i, unitID := range unitIDs {
			if settled[i] != nil {
				continue
			}
			unit, err := o.workers.GetUnit(ctx, batchID, unitID)
			if err != nil {
				return nil, err
			}
			if unit.Status.IsTerminal() {
				settled[i] = unit
				continue
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

func (o *OrchestratorImpl) waitForUnit(ctx context.Context, batchID, unitID string) (*TopologyUnit, error) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		unit, err := o.workers.GetUnit(ctx, batchID, unitID)
		if err != nil {
			return nil, err
		}
		if unit.Status.IsTerminal() {
			return unit, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
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
