package orchestration

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/agentdef"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLoadWorkflowManifestErrors(t *testing.T) {
	stock := extpackstest.StockCatalog(t)
	if _, err := LoadWorkflowManifest(stock, "", ""); err == nil {
		t.Fatal("expected error for empty id")
	}
	// A nil catalog is never "load everything" — it is a hard error.
	if _, err := LoadWorkflowManifest(nil, "plan", "1.0.0"); err == nil {
		t.Fatal("expected nil-catalog error")
	}
	if _, err := LoadWorkflowManifest(stock, "missing", "1.0.0"); err == nil {
		t.Fatal("expected not found error")
	}
	// The version must match the manifest body, not just the id.
	if _, err := LoadWorkflowManifest(stock, "plan", "9.9.9"); err == nil {
		t.Fatal("expected version mismatch to miss")
	}
}

// A disabled workflow unit has no captured bytes, so its manifest is gone from
// the orchestrator's view too — there is no directory left to find it in.
func TestLoadWorkflowManifestHonoursDisable(t *testing.T) {
	content, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent", err)
	desired := extpacks.EmptyDesired()
	desired.Disabled = []string{extpacks.WorkflowUnitID("bugbash")}
	reduced := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: content, Desired: desired})
	if _, err := LoadWorkflowManifest(reduced, "bugbash", "1.1.0"); err == nil {
		t.Fatal("disabled workflow unit must not resolve")
	}
}

func TestTopologyLookupEmpty(t *testing.T) {
	stock := extpackstest.StockCatalog(t)
	if got := TopologyPathForID(nil, "x"); !got.Empty() {
		t.Fatalf("got %q", got)
	}
	if got := TopologyPathForID(stock, ""); !got.Empty() {
		t.Fatalf("got %q", got)
	}
	if got := TopologyPathForID(stock, "does-not-exist"); !got.Empty() {
		t.Fatalf("got %q", got)
	}
	if _, err := TopologySpecForID(nil, "default-pipeline"); err == nil {
		t.Fatal("nil catalog must fail closed")
	}
	if _, err := TopologySpecForID(stock, ""); err == nil {
		t.Fatal("empty topology id must fail")
	}
	if _, err := TopologySpecForID(stock, "does-not-exist"); err == nil {
		t.Fatal("unknown topology must fail")
	}
}

func TestValidateAgentToolProfiles(t *testing.T) {
	reg := NewMemoryAgentRegistryForTest()
	profiles := []sandbox.ToolProfile{
		{ID: "coordinator"},
		{ID: "implement"},
		{ID: "explore_readonly"},
	}
	if err := ValidateAgentToolProfiles(reg, profiles); err != nil {
		testutil.FailErr(t, "ValidateAgentToolProfiles failed", err)
	}
	reg.Register(agentdef.Profile{ID: "broken", ToolProfile: "missing-profile"})
	if err := ValidateAgentToolProfiles(reg, profiles); err == nil {
		t.Fatal("expected missing tool profile error")
	}
	if err := ValidateAgentToolProfiles(nil, profiles); err == nil {
		t.Fatal("expected nil registry error")
	}
}

func TestOrchestratorStatusCancelAndRunErrors(t *testing.T) {
	ctx := context.Background()
	var nilOrch *OrchestratorImpl
	if _, err := nilOrch.Run(ctx, RunRequest{}); err == nil {
		t.Fatal("expected nil orchestrator error")
	}

	reg := NewMemoryAgentRegistryForTest()
	orch := NewOrchestratorImpl(OrchestratorDeps{Agents: reg})
	if _, err := orch.Run(ctx, RunRequest{}); err == nil {
		t.Fatal("expected missing topology/workflow error")
	}
	if _, err := orch.Run(ctx, RunRequest{
		WorkflowID: "default-pipeline",
	}); err == nil {
		t.Fatal("expected missing catalog resolver error")
	}

	stock := extpackstest.StockCatalog(t)
	orch = NewOrchestratorImpl(OrchestratorDeps{
		Agents:  reg,
		Catalog: func() (*extpacks.EffectiveCatalog, error) { return stock, nil },
	})
	if _, err := orch.Run(ctx, RunRequest{
		WorkflowID: "does-not-exist",
		SessionID:  "sess",
	}); err == nil {
		t.Fatal("expected missing workflow error")
	}
	if _, err := orch.Run(ctx, RunRequest{
		Topology: TopologySpec{Pattern: "unknown"},
	}); err == nil {
		t.Fatal("expected unimplemented pattern error")
	}
	if _, err := orch.Status(ctx, "missing"); err == nil {
		t.Fatal("expected status not found error")
	}
	if err := orch.Cancel(ctx, "missing", TerminationReasonHumanAbort); err == nil {
		t.Fatal("expected cancel not found error")
	}
}

func TestValidateTopologyProfilesAllPatterns(t *testing.T) {
	reg := NewMemoryAgentRegistryForTest()
	orch := NewOrchestratorImpl(OrchestratorDeps{Agents: reg})

	spec := &TopologySpec{
		Supervisor: &SupervisorSpec{ProfileIDs: []string{"coordinator", "implementer"}},
	}
	if err := orch.validateTopologyProfiles(spec); err != nil {
		testutil.FailErr(t, "orch.validateTopologyProfiles failed", err)
	}
	spec = &TopologySpec{Pack: &PackSpec{ProfileID: "implementer"}}
	if err := orch.validateTopologyProfiles(spec); err != nil {
		testutil.FailErr(t, "orch.validateTopologyProfiles failed", err)
	}
	spec = &TopologySpec{
		Pipeline: &PipelineSpec{
			Stages: []PipelineStage{{Name: "x", AgentProfile: "missing"}},
		},
	}
	if err := orch.validateTopologyProfiles(spec); err == nil {
		t.Fatal("expected missing profile error")
	}
	nilOrch := NewOrchestratorImpl(OrchestratorDeps{})
	if err := nilOrch.validateTopologyProfiles(spec); err == nil {
		t.Fatal("expected registry not configured error")
	}
}

// loadDefaultPipelineStages reads the bundled default-pipeline.yaml topology — the
// single source of truth for the research → closeout sequence — so tests exercise
// the same stages production loads rather than a parallel Go literal.
func loadDefaultPipelineStages(t *testing.T) []PipelineStage {
	t.Helper()
	spec, err := LoadTopologyFromFile(extpacks.Bundled(config.PlatformFlows.Join("_topologies", "default-pipeline.yaml")))
	testutil.FailErr(t, "LoadTopologyFromFile failed", err)
	if spec.Pipeline == nil {
		t.Fatal("default-pipeline.yaml missing pipeline stages")
	}
	return spec.Pipeline.Stages
}

func TestSetupPipelineDelegationBranches(t *testing.T) {
	ctx := context.Background()
	store := newPipelineStoreStub()
	orch := NewOrchestratorImpl(OrchestratorDeps{
		Store:  store,
		Agents: NewMemoryAgentRegistryForTest(),
	})
	allStages := loadDefaultPipelineStages(t)
	stages := allStages[:1]
	projectDir := t.TempDir()
	state := &runState{stageLegs: make(map[string]string)}

	created, leg := store.seedDelegation("sess-dep", projectDir, "research", ProfileRepoResearcher)
	depID, err := orch.setupPipelineDelegation(ctx, RunRequest{
		Input: map[string]any{"delegation_id": created.ID},
	}, "sess-dep", projectDir, stages, state)
	testutil.FailErr(t, "orch.setupPipelineDelegation failed", err)
	if depID != created.ID {
		t.Fatalf("delegation_id = %q want %q", depID, created.ID)
	}
	if state.stageLegs["research"] != leg.ID {
		t.Fatalf("stage legs = %v", state.stageLegs)
	}

	state = &runState{stageLegs: make(map[string]string)}
	_, err = orch.setupPipelineDelegation(ctx, RunRequest{}, "sess-dep", projectDir, allStages, state)
	if err == nil {
		t.Fatal("expected leg count mismatch error for existing delegation")
	}

	state = &runState{stageLegs: make(map[string]string)}
	depID, err = orch.setupPipelineDelegation(ctx, RunRequest{
		Topology: TopologySpec{Task: "new"},
		Input:    map[string]any{"project_id": testdbseed.DefaultProjectID, "project_dir": projectDir},
	}, "sess-new", projectDir, stages, state)
	testutil.FailErr(t, "orch.setupPipelineDelegation failed", err)
	if depID == "" || len(state.stageLegs) != 1 {
		t.Fatalf("created delegation = %q legs = %v", depID, state.stageLegs)
	}
}

type pipelineStoreStub struct {
	delegations map[string]*api.Delegation
	legs        map[string][]api.Leg
	bySession   map[string]string
}

func newPipelineStoreStub() *pipelineStoreStub {
	return &pipelineStoreStub{
		delegations: make(map[string]*api.Delegation),
		legs:        make(map[string][]api.Leg),
		bySession:   make(map[string]string),
	}
}

func (s *pipelineStoreStub) seedDelegation(sessionID, projectDir, title, agentType string) (*api.Delegation, api.Leg) {
	dep := &api.Delegation{ID: "dep-" + sessionID, ProjectID: testdbseed.DefaultProjectID, WorkspacePath: projectDir, Task: "seed", Strategy: api.HuntStrategyFileBased, Status: "active"}
	leg := api.Leg{ID: "leg-" + title, Title: title, AgentType: agentType, Status: api.LegStatusPending}
	s.delegations[dep.ID] = dep
	s.legs[dep.ID] = []api.Leg{leg}
	s.bySession[sessionID] = dep.ID
	return dep, leg
}

func (s *pipelineStoreStub) Create(ctx context.Context, delegation api.Delegation, sessionID string, legs []api.Leg) (*api.Delegation, error) {
	if delegation.ID == "" {
		delegation.ID = "dep-" + sessionID + "-new"
	}
	created := delegation
	s.delegations[created.ID] = &created
	s.legs[created.ID] = append([]api.Leg(nil), legs...)
	s.bySession[sessionID] = created.ID
	return &created, nil
}

func (s *pipelineStoreStub) GetLeg(ctx context.Context, delegationID, legID string) (*api.Leg, error) {
	for i := range s.legs[delegationID] {
		if s.legs[delegationID][i].ID == legID {
			leg := s.legs[delegationID][i]
			return &leg, nil
		}
	}
	return nil, context.Canceled
}

func (s *pipelineStoreStub) UpdateLeg(ctx context.Context, leg api.Leg) error { return nil }

func (s *pipelineStoreStub) ListLegs(ctx context.Context, delegationID string) ([]api.Leg, error) {
	return append([]api.Leg(nil), s.legs[delegationID]...), nil
}

func (s *pipelineStoreStub) DelegationBySessionID(sessionID string) (string, bool) {
	id, ok := s.bySession[sessionID]
	return id, ok
}

func (s *pipelineStoreStub) DelegationByWorkflowRunID(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func TestPipelineProjectDirAndLegHelpers(t *testing.T) {
	if _, err := pipelineProjectDir(RunRequest{}); err == nil {
		t.Fatal("expected missing project_dir error")
	}
	if dir, err := pipelineProjectDir(RunRequest{Input: map[string]any{"project_dir": " /tmp/x "}}); err != nil || dir != "/tmp/x" {
		t.Fatalf("dir = %q err = %v", dir, err)
	}
	for _, status := range []api.LegStatus{api.LegStatusComplete, api.LegStatusFailed, api.LegStatusCanceled, api.LegStatusRetryPending} {
		if !isSettledLegAttempt(status) {
			t.Fatalf("%s should settle a leg attempt", status)
		}
	}
	if isSettledLegAttempt(api.LegStatusPending) {
		t.Fatal("pending should not settle a leg attempt")
	}
	if copyStringMap(nil) != nil {
		t.Fatal("expected nil copy")
	}
	out := copyStringMap(map[string]string{"a": "b"})
	if out["a"] != "b" {
		t.Fatal(out)
	}
}

func TestValidateGateAgentsBundled(t *testing.T) {
	if err := ValidateGateAgents(nil); err == nil {
		t.Fatal("expected nil registry error")
	}
	reg := NewMemoryAgentRegistry()
	if err := LoadRequiredAgentRegistry(context.Background(), reg); err != nil {
		testutil.FailErr(t, "LoadRequiredAgentRegistry failed", err)
	}
	if err := ValidateGateAgents(reg); err != nil {
		testutil.FailErr(t, "ValidateGateAgents failed", err)
	}
}

func TestLoadTopologyCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	orch := NewOrchestratorImpl(OrchestratorDeps{Agents: NewMemoryAgentRegistryForTest()})
	pipeline := extpacks.Bundled(config.PlatformFlows.Join("_topologies", "default-pipeline.yaml"))
	if _, err := orch.LoadTopology(ctx, pipeline); err == nil {
		t.Fatal("expected canceled context error")
	}
}

func TestWorkflowContextFromInputPartial(t *testing.T) {
	if workflowContextFromInput(RunRequest{}) != nil {
		t.Fatal("expected nil")
	}
	wf := workflowContextFromInput(RunRequest{Input: map[string]any{
		"workflow_run_id": "run-1",
		"workflow_id":     "default-pipeline",
	}})
	if wf == nil || wf.runID != "run-1" || wf.id != "default-pipeline" {
		t.Fatalf("wf = %+v", wf)
	}
}

func (s *pipelineStoreStub) Get(_ context.Context, id string) (*api.Delegation, error) {
	if delegation, ok := s.delegations[id]; ok {
		copy := *delegation
		return &copy, nil
	}
	return nil, fmt.Errorf("delegation %q not found", id)
}
