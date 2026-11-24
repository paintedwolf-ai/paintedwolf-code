package promptrender_test

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
	"github.com/lycaon/lycaon/test/contract/internal/oarproducers"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

type stepCheckFilter func(ruleID, stepPath, action string) bool

type staticWorkerDecisions struct {
	req api.WorkerDecisionRequest
	ok  bool
}

func (d staticWorkerDecisions) GetByJob(_ context.Context, _ string) (api.WorkerDecisionRequest, bool, error) {
	return d.req, d.ok, nil
}

func closeExercisedEdgeScopes(
	t *testing.T,
	s *promptrender.Stock,
	ledger *interactions.InteractionLedger,
	reps map[string]jobWaitAuthorityRep,
	skipStep stepCheckFilter,
) {
	t.Helper()
	coordRep, hasCoord := reps["route=callable|wait_process_done=true"]
	if !hasCoord {
		t.Fatal("missing callable coordinator rep for edge assertions")
	}
	coordStates, err := hoststate.CoordinatorStates(t.Context())
	if err != nil {
		t.Fatalf("CoordinatorStates: %v", err)
	}
	workerStates, err := hoststate.WorkerStates(t.Context())
	if err != nil {
		t.Fatalf("WorkerStates: %v", err)
	}
	workerRep := pickWorkerRepForEdge(t, workerStates, "implementer", api.SessionPostureBuild)
	readonlyWorkerRep := pickWorkerRepForEdge(t, workerStates, "plan-reviewer", api.SessionPostureSpec)

	injR := prompts.NewInjectRenderer(s.Engine())
	now := time.Now().UTC()
	runJobs := jobSnapshotsForOccurrence(occRunning)
	heldSlice := []heldcall.Running{{Handle: "held-1", Tool: "command", Elapsed: 8 * time.Second}}
	for _, entry := range []struct {
		recip string
		rep   jobWaitAuthorityRep
	}{
		{"coordinator", coordRep},
		{"worker", workerRep},
	} {
		runBlock := inject.RenderCommandJobsBlock(t.Context(), injR, entry.rep.auth.Reader, entry.rep.auth.Schemas["wait"], "sess-edge", runJobs, nil, now, entry.recip)
		if err := verifyIndependentJobWaitExpectedActions(s.Names, []string{runBlock}, runJobs, entry.rep.auth); err != nil {
			t.Fatalf("%s running job assertion failed: %v", entry.recip, err)
		}
		wantStop := slices.Contains(entry.rep.auth.Offered, "command_stop")
		gotStop := len(parsedCallsForTool(s.Names, []string{runBlock}, "command_stop")) > 0
		if gotStop != wantStop {
			t.Fatalf("%s command_stop=%t, want %t", entry.recip, gotStop, wantStop)
		}
		heldBlock := inject.RenderCommandJobsBlock(t.Context(), injR, entry.rep.auth.Reader, entry.rep.auth.Schemas["wait"], "sess-held", nil, heldSlice, now, entry.recip)
		wantHeldRes := slices.Contains(entry.rep.auth.Offered, "held_result")
		gotHeldRes := len(parsedCallsForTool(s.Names, []string{heldBlock}, "held_result")) > 0
		if gotHeldRes != wantHeldRes {
			t.Fatalf("%s held_result=%t, want %t", entry.recip, gotHeldRes, wantHeldRes)
		}
	}

	promoteSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
		return !c.Excluded && c.PackOverlayID == "stock" && c.SessionMode == api.SessionModeAgent &&
			c.Posture == api.SessionPostureBuild && c.TurnProfile.SurfaceID == "implement_overlay_promote" &&
			c.Loading == hoststate.LoadingAllLoaded && c.RootCount == 1
	})
	promoteAuth, err := hoststate.FreezeTurnAuthority(t.Context(), promoteSt)
	if err != nil {
		t.Fatalf("FreezeTurnAuthority(promote): %v", err)
	}
	exerciseWorkerTaskFinishedGuidance(t, s, ledger, promoteSt, promoteAuth)

	synthSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
		return !c.Excluded && c.PackOverlayID == "stock" && c.SessionMode == api.SessionModeAgent &&
			c.Posture == api.SessionPostureBuild && c.TurnProfile.SurfaceID == "implement_synthesis" &&
			c.RootCount == 1
	})
	synthAuth, _ := hoststate.FreezeTurnAuthority(t.Context(), synthSt)
	investSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
		return !c.Excluded && c.PackOverlayID == "stock" && c.SessionMode == api.SessionModeAgent &&
			c.Posture == api.SessionPostureBuild && c.TurnProfile.SurfaceID == "implement_investigate" &&
			c.Loading == hoststate.LoadingAllLoaded && c.RootCount == 1
	})
	investAuth, _ := hoststate.FreezeTurnAuthority(t.Context(), investSt)
	floorInvestSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
		return !c.Excluded && c.PackOverlayID == "stock" && c.SessionMode == api.SessionModeAgent &&
			c.Posture == api.SessionPostureBuild && c.TurnProfile.SurfaceID == "implement_investigate" &&
			c.Loading == hoststate.LoadingFloorOnly && c.RootCount == 1
	})
	floorInvestAuth, _ := hoststate.FreezeTurnAuthority(t.Context(), floorInvestSt)
	specFloorInvestSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
		return !c.Excluded && c.PackOverlayID == "stock" && c.SessionMode == api.SessionModeAgent &&
			c.Posture == api.SessionPostureSpec && c.TurnProfile.SurfaceID == "implement_investigate" &&
			c.Loading == hoststate.LoadingFloorOnly && c.RootCount == 1
	})
	specFloorInvestAuth, _ := hoststate.FreezeTurnAuthority(t.Context(), specFloorInvestSt)
	deferredAskAuth := buildDeferredAskUserAuthority(t, s, investSt, investAuth)

	oarH, err := oarproducers.NewHarness(s.Catalog, s.Schemas, s.Engine())
	if err != nil {
		t.Fatalf("oarproducers.NewHarness: %v", err)
	}
	oarCells := []oarproducers.AuthCell{
		{Label: investSt.ID, Role: "coordinator", SurfaceID: investSt.TurnProfile.SurfaceID, ProfileID: "coordinator", Mode: investSt.SessionMode, Posture: investSt.Posture, Auth: investAuth, Reach: investAuth.Reach},
		{Label: investSt.ID + "+deferred_ask_user", Role: "coordinator", SurfaceID: investSt.TurnProfile.SurfaceID, ProfileID: "coordinator", Mode: api.SessionModeAgent, Posture: api.SessionPostureBuild, Auth: deferredAskAuth, Reach: deferredAskAuth.Reach},
		{Label: floorInvestSt.ID, Role: "coordinator", SurfaceID: floorInvestSt.TurnProfile.SurfaceID, ProfileID: "coordinator", Mode: floorInvestSt.SessionMode, Posture: floorInvestSt.Posture, Auth: floorInvestAuth, Reach: floorInvestAuth.Reach},
		{Label: specFloorInvestSt.ID, Role: "coordinator", SurfaceID: specFloorInvestSt.TurnProfile.SurfaceID, ProfileID: "coordinator", Mode: specFloorInvestSt.SessionMode, Posture: specFloorInvestSt.Posture, Auth: specFloorInvestAuth, Reach: specFloorInvestAuth.Reach},
		{Label: promoteSt.ID, Role: "coordinator", SurfaceID: promoteSt.TurnProfile.SurfaceID, ProfileID: "coordinator", Mode: promoteSt.SessionMode, Posture: promoteSt.Posture, Auth: promoteAuth, Reach: promoteAuth.Reach},
		{Label: synthSt.ID, Role: "coordinator", SurfaceID: synthSt.TurnProfile.SurfaceID, ProfileID: "coordinator", Mode: synthSt.SessionMode, Posture: synthSt.Posture, Auth: synthAuth, Reach: synthAuth.Reach},
		{Label: workerRep.label, Role: "worker", AgentType: workerRep.workerState.AgentID, ProfileID: workerRep.workerState.ToolProfile, Mode: workerRep.workerState.ParentSessionMode, Posture: workerRep.workerState.Posture, Auth: workerRep.auth, Reach: workerRep.auth.Reach},
		{Label: readonlyWorkerRep.label, Role: "worker", AgentType: readonlyWorkerRep.workerState.AgentID, ProfileID: readonlyWorkerRep.workerState.ToolProfile, Mode: readonlyWorkerRep.workerState.ParentSessionMode, Posture: readonlyWorkerRep.workerState.Posture, Auth: readonlyWorkerRep.auth, Reach: readonlyWorkerRep.auth.Reach},
	}
	rejOut, err := oarH.RunToolRejectedProducers(t.Context(), oarCells)
	if err != nil {
		t.Fatalf("RunToolRejectedProducers: %v", err)
	}
	lifeOut, err := oarH.RunLifecycleHookProducers(t.Context(), oarCells)
	if err != nil {
		t.Fatalf("RunLifecycleHookProducers: %v", err)
	}
	workerReachableTools := collectWorkerReachableTools(t, workerStates)
	assertOARProducerEdgeOutcomes(
		t, s, ledger, append(rejOut, lifeOut...),
		investSt.ID, floorInvestSt.ID, specFloorInvestSt.ID,
		workerRep.label, readonlyWorkerRep.label,
		workerReachableTools, skipStep,
	)
}

func exerciseWorkerTaskFinishedGuidance(
	t *testing.T,
	s *promptrender.Stock,
	ledger *interactions.InteractionLedger,
	promoteSt hoststate.CoordinatorState,
	promoteAuth hoststate.TurnAuthority,
) {
	t.Helper()
	moduleRoot := configlayout.FindModuleRoot()
	injR := prompts.NewInjectRenderer(s.Engine())
	askPromoteAuth := buildReadOnlyOverlayInspectAuthority(t, s, promoteSt, promoteAuth)

	var wfSites []interactions.InstructionSite
	for _, site := range ledger.DiscoveredInstructions {
		if site.ContributionID == "guidance/coordinator-worker-task-finished" {
			wfSites = append(wfSites, site)
		}
	}

	cases := []struct {
		label  string
		mode   api.SessionMode
		auth   hoststate.TurnAuthority
		notice map[string]any
		hasDec bool
	}{
		{
			label:  promoteSt.ID + "|changed_with_queued",
			mode:   api.SessionModeAgent,
			auth:   promoteAuth,
			notice: map[string]any{"worker": "impl-1", "leg_status": "partial", "changed": []string{"internal/a.go"}, "not_applied": []string{"check edge"}, "result_handle": "leg-1"},
			hasDec: true,
		},
		{
			label:  promoteSt.ID + "|unchanged",
			mode:   api.SessionModeAgent,
			auth:   promoteAuth,
			notice: map[string]any{"worker": "impl-1", "leg_status": "complete", "changed": []string{}, "not_applied": []string{}, "result_handle": "leg-2"},
			hasDec: false,
		},
		{
			label:  promoteSt.ID + "+readonly_overlay_inspect|changed",
			mode:   api.SessionModeAsk,
			auth:   askPromoteAuth,
			notice: map[string]any{"worker": "impl-1", "leg_status": "complete", "changed": []string{"internal/a.go"}, "not_applied": []string{}, "result_handle": "leg-3"},
			hasDec: false,
		},
	}

	for idx, tc := range cases {
		var observedEdges []string
		results := workeroutcomes.NewResults(workeroutcomes.ResultPorts{})
		results.SetResultNotices(func(_ context.Context, _ string) (map[string]any, bool, error) {
			observedEdges = append(observedEdges, oarproducers.ObserveIndirectProducerCall(
				moduleRoot, "worker_result", "worker_result.changed", "worker_result.not_applied", "worker_result.result_handle",
			)...)
			return maps.Clone(tc.notice), true, nil
		})
		if tc.hasDec {
			results.SetDecisions(staticWorkerDecisions{
				ok: true,
				req: api.WorkerDecisionRequest{
					WorkerID:     fmt.Sprintf("job-%d", idx),
					Question:     "Proceed with migration?",
					BlockerClass: "decision",
				},
			})
			results.SetWorkerNames(func(_ context.Context, _ string) (string, bool, error) {
				observedEdges = append(observedEdges, oarproducers.ObserveIndirectProducerCall(
					moduleRoot, "last_worker_decision_request", "last_worker_decision_request.worker",
				)...)
				return "impl-1", true, nil
			})
		}

		env := results.EnvelopeForTerminal(t.Context(), "sess-wf", fmt.Sprintf("job-%d", idx))
		vars := maps.Clone(env.Vars)
		if vars == nil {
			vars = map[string]any{}
		}
		if len(env.WorkerDecisionRequest) > 0 {
			vars["last_worker_decision_request"] = env.WorkerDecisionRequest
		}
		wfText, rErr := injR.RenderWithReader(t.Context(), tc.auth.Reader, "coordinator-worker-task-finished", vars)
		if rErr != nil {
			t.Fatalf("RenderWithReader coordinator-worker-task-finished (%s): %v", tc.label, rErr)
		}
		if viols := promptrender.EvaluateClaimsForTest(s, "guidance/coordinator-worker-task-finished ("+tc.label+")", wfText, tc.auth, tc.mode, promoteSt.Posture, toolvocabtest.SubjectSelfNow(), false); len(viols) > 0 {
			t.Fatalf("coordinator-worker-task-finished claim violation (%s): %v", tc.label, viols[0])
		}

		presentActions := renderedActionsInProse(s.Names, wfText)
		for _, site := range wfSites {
			baseTool := site.Action
			if cut, _, ok := strings.Cut(baseTool, "("); ok {
				baseTool = cut
			}
			if !presentActions[site.Action] || !slices.Contains(tc.auth.Offered, baseTool) {
				continue
			}
			for _, eid := range observedEdges {
				edgeFact, _, _ := strings.Cut(eid, "@")
				if slices.Contains(site.UnresolvedFacts, edgeFact) {
					ledger.RecordExecutedEdgeAssertion(interactions.ExecutedEdgeAssertion{
						EdgeID:         eid,
						InstructionID:  site.ID,
						Recipient:      "coordinator",
						InputState:     tc.label,
						AssertedAction: site.Action,
						CheckedOutcome: "emitted",
						TestName:       t.Name(),
					})
				}
			}
		}
	}
}

func renderedActionsInProse(names *toolvocab.Catalog, text string) map[string]bool {
	out := map[string]bool{}
	for _, ex := range toolvocabtest.ParseCallExamples(names, text) {
		if ex.Tool == "" {
			continue
		}
		out[ex.Tool] = true
		switch ex.Tool {
		case "request_tools":
			if need, ok := ex.Args["need"].(string); ok && need != "" {
				out["request_tools(need="+need+")"] = true
			}
		case "wait":
			if strings.Contains(ex.Raw, "process_done") {
				out["wait(process_done)"] = true
			}
		case "command":
			if cmdStr, ok := ex.Args["command"].(string); ok {
				if fields := strings.Fields(cmdStr); len(fields) > 0 {
					out["command:"+fields[0]] = true
				}
			}
		}
	}
	for _, tool := range toolvocab.ToolsIn(names, text) {
		out[tool] = true
	}
	return out
}

func buildReadOnlyOverlayInspectAuthority(
	t *testing.T,
	s *promptrender.Stock,
	baseSt hoststate.CoordinatorState,
	baseAuth hoststate.TurnAuthority,
) hoststate.TurnAuthority {
	t.Helper()
	offered := []string{"recall", "preview_overlay", "worker_history", "wait"}
	return buildCustomCoordinatorAuthority(t, s, baseSt, baseAuth, offered, nil, api.SessionModeAsk)
}

func buildDeferredAskUserAuthority(
	t *testing.T,
	s *promptrender.Stock,
	baseSt hoststate.CoordinatorState,
	baseAuth hoststate.TurnAuthority,
) hoststate.TurnAuthority {
	t.Helper()
	offered := []string{"recall", "request_tools", "secret_generate"}
	return buildCustomCoordinatorAuthority(t, s, baseSt, baseAuth, offered, []string{"ask_user"}, api.SessionModeAgent)
}

func buildCustomCoordinatorAuthority(
	t *testing.T,
	s *promptrender.Stock,
	baseSt hoststate.CoordinatorState,
	baseAuth hoststate.TurnAuthority,
	offered []string,
	deferred []string,
	mode api.SessionMode,
) hoststate.TurnAuthority {
	t.Helper()
	overlay := baseSt.Overlay
	if overlay.Catalog == nil {
		overlays, err := hoststate.DiscoverPackOverlays(t.Context())
		if err != nil {
			t.Fatalf("DiscoverPackOverlays: %v", err)
		}
		overlay = overlays[0]
	}
	_, narrowed, err := hoststate.ExecutorForOverlay(overlay, baseSt.Posture, mode == api.SessionModeAsk, baseSt.WebSearch, baseSt.CommandExternalHidden, baseSt.MCPState)
	if err != nil {
		t.Fatalf("ExecutorForOverlay: %v", err)
	}
	plan := toolsurface.Compile(offered, deferred)
	offeredSchemas := map[string]map[string]any{}
	var offeredMetas []tools.ToolMeta
	for _, m := range baseAuth.Metas {
		if slices.Contains(offered, m.Name) {
			offeredMetas = append(offeredMetas, m)
			if sc, ok := baseAuth.Schemas[m.Name]; ok {
				offeredSchemas[m.Name] = sc
			}
		}
	}
	postLoadSchemas := maps.Clone(offeredSchemas)
	for _, d := range deferred {
		if sc, ok := baseAuth.Schemas[d]; ok {
			postLoadSchemas[d] = sc
		}
	}
	reader, err := prompts.NewReader(prompts.HostFacts{
		SessionMode:   string(mode),
		Profiles:      narrowed,
		Schemas:       s.Schemas,
		RootCount:     1,
		HasGit:        true,
		HasAnyGitRoot: true,
	}, prompts.TurnProjection{
		ProfileID:        prompts.CoordinatorProfileID,
		SurfaceID:        baseSt.TurnProfile.SurfaceID,
		Plan:             plan,
		Offered:          offered,
		ProjectedSchemas: offeredSchemas,
	})
	if err != nil {
		t.Fatalf("prompts.NewReader: %v", err)
	}
	return hoststate.TurnAuthority{
		Metas:           offeredMetas,
		Schemas:         offeredSchemas,
		PostLoadSchemas: postLoadSchemas,
		Offered:         slices.Clone(offered),
		Plan:            plan,
		ProgramReach:    baseAuth.ProgramReach,
		ProgramTable:    baseAuth.ProgramTable,
		Reach:           toolreach.Freeze(offered, plan).WithSchemas(offeredSchemas),
		Reader:          reader,
	}
}

func collectWorkerReachableTools(t *testing.T, workerStates []hoststate.WorkerState) map[string]bool {
	t.Helper()
	reachable := map[string]bool{}
	for _, ws := range workerStates {
		if ws.Excluded || ws.Overlay.ID != "stock" {
			continue
		}
		auth, err := hoststate.FreezeWorkerAuthority(t.Context(), ws)
		if err != nil {
			continue
		}
		for _, tool := range auth.Offered {
			reachable[tool] = true
		}
		for _, tool := range auth.Reader.Requestable() {
			reachable[tool] = true
		}
	}
	return reachable
}

type ambientStepEvidence struct {
	emittedState string
	omittedState string
	omittedEdges []string
}

func assertOARProducerEdgeOutcomes(
	t *testing.T,
	s *promptrender.Stock,
	ledger *interactions.InteractionLedger,
	outcomes []oarproducers.FiredOutcome,
	taskCallableLabel, taskDeferredLabel, specFloorLabel string,
	surfaceWorkerLabel, nonSurfaceWorkerLabel string,
	workerReachableTools map[string]bool,
	skipStep stepCheckFilter,
) {
	t.Helper()
	var sawMidTurnTaskCallable, sawMidTurnTaskDeferred bool
	var sawSurfaceWorkerHandles, sawNonSurfaceWorkerFallback bool
	var sawBuildSandboxCap, sawSpecSandboxOmitted bool
	ambientByStep := map[string]*ambientStepEvidence{}

	for _, o := range outcomes {
		if o.CheckClaims && strings.TrimSpace(o.FixCopy) != "" {
			if viols := promptrender.EvaluateClaimsForTest(s, "edge-oar/"+o.RuleID, o.FixCopy, o.Cell.Auth, o.Cell.Mode, o.Cell.Posture, toolvocabtest.SubjectSelfNow(), false); len(viols) > 0 {
				t.Fatalf("OAR edge assertion %s failed on %s: %v", o.RuleID, o.Cell.Label, viols[0])
			}
		}
		shortID := o.RuleID
		if idx := strings.LastIndexByte(shortID, '/'); idx >= 0 {
			shortID = shortID[idx+1:]
		}
		if shortID == "SANDBOX_TRY_CREDENTIAL_STORE" {
			if o.Cell.Label == taskCallableLabel && strings.Contains(o.FixCopy, "capability_request.credential_store") {
				sawBuildSandboxCap = true
			}
			if o.Cell.Label == specFloorLabel {
				if strings.Contains(o.FixCopy, "capability_request.credential_store") {
					t.Fatalf("expected SANDBOX_TRY_CREDENTIAL_STORE on spec %s to omit unoffered capability_request.credential_store, got %q", o.Cell.Label, o.FixCopy)
				}
				sawSpecSandboxOmitted = true
			}
		}
		if shortID == "MID_TURN_NOTE_UNGROUNDED" {
			if o.Cell.Label == taskCallableLabel {
				if !strings.Contains(o.FixCopy, "[task#1]") {
					t.Fatalf("expected MID_TURN_NOTE_UNGROUNDED on task-offering %s to cite [task#1], got %q", o.Cell.Label, o.FixCopy)
				}
				sawMidTurnTaskCallable = true
			}
			if o.Cell.Label == taskDeferredLabel {
				if strings.Contains(o.FixCopy, "[task#1]") || !strings.Contains(o.FixCopy, "[kind#ordinal]") {
					t.Fatalf("expected MID_TURN_NOTE_UNGROUNDED on floor-only %s to omit [task#1] and cite [kind#ordinal], got %q", o.Cell.Label, o.FixCopy)
				}
				sawMidTurnTaskDeferred = true
			}
		}
		if shortID == "SURFACE_CLAIM_UNGROUNDED" {
			if o.Cell.Label == surfaceWorkerLabel {
				if !strings.Contains(o.FixCopy, "page#") || !strings.Contains(o.FixCopy, "tui#") || !strings.Contains(o.FixCopy, "terminal_session#") {
					t.Fatalf("expected SURFACE_CLAIM_UNGROUNDED on surface-capable %s to cite page#, tui#, terminal_session#, got %q", o.Cell.Label, o.FixCopy)
				}
				sawSurfaceWorkerHandles = true
			}
			if o.Cell.Label == nonSurfaceWorkerLabel {
				if strings.Contains(o.FixCopy, "page#") || strings.Contains(o.FixCopy, "tui#") || strings.Contains(o.FixCopy, "terminal_session#") || !strings.Contains(o.FixCopy, "[kind#ordinal]") {
					t.Fatalf("expected SURFACE_CLAIM_UNGROUNDED on non-surface %s to omit unmintable handles and cite [kind#ordinal], got %q", o.Cell.Label, o.FixCopy)
				}
				sawNonSurfaceWorkerFallback = true
			}
		}

		if !o.CheckClaims {
			continue
		}
		recordVerifiedStepEvidence(t, s, ledger, o, shortID, workerReachableTools, ambientByStep, skipStep)
	}

	for key, ev := range ambientByStep {
		if ev.emittedState == "" || ev.omittedState == "" || len(ev.omittedEdges) == 0 {
			continue
		}
		recip, instrID, _ := strings.Cut(key, "|")
		_, action, _ := strings.Cut(instrID, "||")
		for _, eid := range ev.omittedEdges {
			ledger.RecordExecutedEdgeAssertion(interactions.ExecutedEdgeAssertion{
				EdgeID:         eid,
				InstructionID:  instrID,
				Recipient:      recip,
				InputState:     ev.omittedState + " && " + ev.emittedState,
				AssertedAction: action,
				CheckedOutcome: "emitted_when_zero+omitted_when_positive",
				TestName:       t.Name(),
			})
		}
	}

	if !sawMidTurnTaskCallable || !sawMidTurnTaskDeferred {
		t.Fatalf("missing paired MID_TURN_NOTE_UNGROUNDED witnesses: callable=%t deferred=%t", sawMidTurnTaskCallable, sawMidTurnTaskDeferred)
	}
	if !sawSurfaceWorkerHandles || !sawNonSurfaceWorkerFallback {
		t.Fatalf("missing paired SURFACE_CLAIM_UNGROUNDED witnesses: surface=%t non_surface=%t", sawSurfaceWorkerHandles, sawNonSurfaceWorkerFallback)
	}
	if !sawBuildSandboxCap || !sawSpecSandboxOmitted {
		t.Fatalf("missing paired SANDBOX_TRY_CREDENTIAL_STORE witnesses: build=%t spec_omitted=%t", sawBuildSandboxCap, sawSpecSandboxOmitted)
	}
}

func callsSliceEqual(a, b []toolreach.Call) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !reflect.DeepEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func stepActionRendered(st oarproducers.FiredStepOutcome, stepActions map[string]bool) bool {
	if stepActions[st.Action] || (st.Tool != "" && stepActions[st.Tool]) {
		return true
	}
	if st.Program != "" {
		return strings.Contains(st.RenderedText, "`"+st.Program+" ") ||
			strings.Contains(st.RenderedText, "\""+st.Program+" ") ||
			strings.Contains(st.RenderedText, "`"+st.Program+"`")
	}
	return false
}

func recordVerifiedStepEvidence(
	t *testing.T,
	s *promptrender.Stock,
	ledger *interactions.InteractionLedger,
	o oarproducers.FiredOutcome,
	shortID string,
	workerReachableTools map[string]bool,
	ambientByStep map[string]*ambientStepEvidence,
	skipStep stepCheckFilter,
) {
	t.Helper()
	if !o.HasResolvedSteps || len(o.Steps) == 0 {
		return
	}
	copyBody := o.ActionCopy
	if strings.TrimSpace(copyBody) == "" {
		copyBody = o.FixCopy
	}
	presentInCopy := renderedActionsInProse(s.Names, copyBody)
	inputState := o.Cell.Label + "|" + o.EventLabel

	type callSpan struct{ start, end int }
	spans := make([]callSpan, len(o.Steps))
	var expectedCalls []toolreach.Call
	var expectedTexts []string
	for i, st := range o.Steps {
		spans[i].start = len(expectedCalls)
		if st.Outcome == oar.StepOutcomeEmittedCall {
			expectedCalls = append(expectedCalls, st.SelectedCalls...)
		}
		spans[i].end = len(expectedCalls)
		if (st.Outcome == oar.StepOutcomeEmittedCall || st.Outcome == oar.StepOutcomeEmittedOtherwise) && strings.TrimSpace(st.RenderedText) != "" {
			expectedTexts = append(expectedTexts, st.RenderedText)
		}
	}
	joinedText := strings.TrimSpace(strings.Join(expectedTexts, " "))
	callsDelivered := callsSliceEqual(expectedCalls, o.NextCalls)
	joinedTextDelivered := joinedText == "" || strings.Contains(copyBody, joinedText)

	for i, st := range o.Steps {
		if st.InstructionID == "" {
			continue
		}
		if skipStep != nil && skipStep(shortID, st.StepPath, st.Action) {
			continue
		}
		trimmedStepText := strings.TrimSpace(st.RenderedText)
		stepTextDelivered := trimmedStepText != "" && joinedTextDelivered && strings.Contains(copyBody, trimmedStepText)
		stepCallsDelivered := callsDelivered && spans[i].end > spans[i].start && spans[i].end <= len(o.NextCalls) &&
			callsSliceEqual(st.SelectedCalls, o.NextCalls[spans[i].start:spans[i].end])
		stepActions := renderedActionsInProse(s.Names, st.RenderedText)

		ambKey := o.Cell.Role + "|" + st.InstructionID
		if shortID == "AMBIENT_UNGROUNDED_COMPLETION" {
			ev := ambientByStep[ambKey]
			if ev == nil {
				ev = &ambientStepEvidence{}
				ambientByStep[ambKey] = ev
			}
			if st.Outcome == oar.StepOutcomeEmittedCall && st.GuardApplies && st.Reachable &&
				stepCallsDelivered && stepTextDelivered && stepActionRendered(st, stepActions) &&
				slices.Contains(o.Cell.Auth.Offered, st.Tool) && ev.emittedState == "" {
				ev.emittedState = inputState
			} else if st.Outcome == oar.StepOutcomeDisabled && st.HasGuard && !st.GuardApplies && !st.Reachable &&
				len(st.SelectedCalls) == 0 && trimmedStepText == "" && !presentInCopy[st.Tool] &&
				len(o.ExercisedEdges) > 0 && ev.omittedState == "" {
				ev.omittedState = inputState
				ev.omittedEdges = slices.Clone(o.ExercisedEdges)
			}
			continue
		}

		if st.Outcome == oar.StepOutcomeEmittedCall {
			for _, mc := range st.SelectedCalls {
				isOffered := slices.Contains(o.Cell.Auth.Offered, mc.Tool)
				isLoadable := o.Cell.Reach.Status(mc.Tool) == toolreach.Loadable
				if !isOffered && !isLoadable {
					t.Fatalf("step %s emitted unreachable tool %s on %s", st.InstructionID, mc.Tool, o.Cell.Label)
				}
			}
			if !st.GuardApplies || !st.Reachable || !stepCallsDelivered || !stepTextDelivered || !stepActionRendered(st, stepActions) {
				continue
			}
			for _, eid := range o.ExercisedEdges {
				ledger.RecordExecutedEdgeAssertion(interactions.ExecutedEdgeAssertion{
					EdgeID:         eid,
					InstructionID:  st.InstructionID,
					Recipient:      o.Cell.Role,
					InputState:     inputState,
					AssertedAction: st.Action,
					CheckedOutcome: "emitted",
					TestName:       t.Name(),
				})
			}
			continue
		}

		targetTool := st.Tool
		if strings.HasPrefix(st.Action, "request_tools(need=") {
			targetTool = strings.TrimSuffix(strings.TrimPrefix(st.Action, "request_tools(need="), ")")
		}
		if o.Cell.Role == "worker" && !workerReachableTools[targetTool] &&
			!presentInCopy[st.Action] && !presentInCopy[targetTool] && len(st.SelectedCalls) == 0 {
			checked := ""
			if st.Otherwise != "" {
				if st.Outcome == oar.StepOutcomeEmittedOtherwise && st.GuardApplies && !st.Reachable && stepTextDelivered {
					checked = "omitted_otherwise"
				}
			} else if (st.Outcome == oar.StepOutcomeOmitted || st.Outcome == oar.StepOutcomeDisabled) && trimmedStepText == "" {
				checked = "omitted_unavailable"
			}
			if checked != "" {
				for _, eid := range o.ExercisedEdges {
					ledger.RecordExecutedEdgeAssertion(interactions.ExecutedEdgeAssertion{
						EdgeID:         eid,
						InstructionID:  st.InstructionID,
						Recipient:      o.Cell.Role,
						InputState:     inputState,
						AssertedAction: st.Action,
						CheckedOutcome: checked,
						TestName:       t.Name(),
					})
				}
			}
		}
	}
}

// TestDisposableControlRetainsFiredRuleAndSiblingWhileFailingRemovedStepOrCheck
// verifies that step provenance preserves each step's own obligation across
// suppressed targets, skipped checks, identical-argument same-tool siblings,
// unselected alternatives, and undelivered step text.
func TestDisposableControlRetainsFiredRuleAndSiblingWhileFailingRemovedStepOrCheck(t *testing.T) {
	s := mustStock(t)
	moduleRoot := configlayout.FindModuleRoot()
	origStepsBytes, err := os.ReadFile(filepath.Join(moduleRoot, "config", "packs", "painted-wolf", "security", "host", "next-steps", "CREDENTIAL_LITERAL.yaml"))
	if err != nil {
		t.Fatalf("read stock next-steps CREDENTIAL_LITERAL.yaml: %v", err)
	}
	origPolicyBytes, err := os.ReadFile(filepath.Join(moduleRoot, "config", "packs", "painted-wolf", "security", "policy", "CREDENTIAL_LITERAL.yaml"))
	if err != nil {
		t.Fatalf("read stock policy CREDENTIAL_LITERAL.yaml: %v", err)
	}
	origSteps := string(origStepsBytes)
	origPolicy := string(origPolicyBytes)

	coordStates, err := hoststate.CoordinatorStates(t.Context())
	if err != nil {
		t.Fatalf("CoordinatorStates: %v", err)
	}
	callableSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
		return !c.Excluded && c.PackOverlayID == "stock" && c.SessionMode == api.SessionModeAgent &&
			c.Posture == api.SessionPostureBuild && c.TurnProfile.SurfaceID == "implement_overlay_promote" &&
			c.Loading == hoststate.LoadingAllLoaded && c.RootCount == 1
	})
	callableAuth, err := hoststate.FreezeTurnAuthority(t.Context(), callableSt)
	if err != nil {
		t.Fatalf("FreezeTurnAuthority: %v", err)
	}
	reps := map[string]jobWaitAuthorityRep{
		"route=callable|wait_process_done=true": {
			label: callableSt.ID, coordState: callableSt, auth: callableAuth, route: routeCallable, waitAdmitted: true,
		},
	}

	const (
		targetInstr         = "oar:credential.assignment:CREDENTIAL_LITERAL|policy/CREDENTIAL_LITERAL#next-steps:steps[0]||secret_generate"
		askSiblingInstr     = "oar:credential.assignment:CREDENTIAL_LITERAL|policy/CREDENTIAL_LITERAL#next-steps:steps[1]||ask_user"
		sameToolSiblingInst = "oar:credential.assignment:CREDENTIAL_LITERAL|policy/CREDENTIAL_LITERAL#next-steps:steps[3]||secret_generate"
		unselectedOrInstr   = "oar:credential.assignment:CREDENTIAL_LITERAL|policy/CREDENTIAL_LITERAL#next-steps:steps[0].or[0]||secret_generate"
		weakSiblingInstr    = "oar:credential.assignment:WEAK_CREDENTIAL_LITERAL|policy/WEAK_CREDENTIAL_LITERAL#next-steps:steps[0]||secret_generate"
		origStepZeroBlock   = "  - tool: \"secret_generate\"\n" +
			"    say: \"For a new credential, use {{ call }} and carry its returned reference.\"\n" +
			"    otherwise: \"If you need a new credential, report that need to the coordinating session for protected generation.\"\n"
	)

	var diagReport strings.Builder

	t.Run("disposable_catalog_suppresses_target_step_action", func(t *testing.T) {
		mutatedSteps := strings.Replace(
			origSteps,
			"  - tool: \"secret_generate\"\n",
			"  - tool: \"secret_generate\"\n    when: \"paintedwolf.listed\"\n",
			1,
		)
		if mutatedSteps == origSteps {
			t.Fatal("failed to mutate steps[0] in CREDENTIAL_LITERAL next-steps")
		}
		dispEff := buildDisposableSecurityCatalog(t, moduleRoot, mutatedSteps, "")
		dispStock := cloneStockWithCatalog(t, s, dispEff)
		dispLedger, err := interactions.BuildInteractionLedger(dispEff, s.Schemas, s.Names, nil)
		if err != nil {
			t.Fatalf("BuildInteractionLedger(disposable): %v", err)
		}
		closeExercisedEdgeScopes(t, dispStock, dispLedger, reps, nil)
		valErr := dispLedger.ValidateZeroUnexplainedGaps()
		assertTargetFailedAndSiblingRetained(t, dispLedger, valErr, targetInstr, askSiblingInstr, "ask_user", "disposable_catalog_suppresses_target_step_action", &diagReport)
	})

	t.Run("stock_catalog_skips_target_step_check", func(t *testing.T) {
		skipLedger, err := interactions.BuildInteractionLedger(s.Catalog, s.Schemas, s.Names, nil)
		if err != nil {
			t.Fatalf("BuildInteractionLedger(stock): %v", err)
		}
		closeExercisedEdgeScopes(t, s, skipLedger, reps, func(ruleID, stepPath, action string) bool {
			return ruleID == "CREDENTIAL_LITERAL" && stepPath == "steps[0]" && action == "secret_generate"
		})
		valErr := skipLedger.ValidateZeroUnexplainedGaps()
		assertTargetFailedAndSiblingRetained(t, skipLedger, valErr, targetInstr, askSiblingInstr, "ask_user", "stock_catalog_skips_target_step_check", &diagReport)
	})

	t.Run("disposable_same_tool_identical_args_sibling_retains_own_credit_while_suppressed_target_fails", func(t *testing.T) {
		suppressedTargetWithArgs := "  - tool: \"secret_generate\"\n" +
			"    when: \"paintedwolf.listed\"\n" +
			"    args:\n" +
			"      format: \"base64url\"\n" +
			"      scope: \"chat\"\n" +
			"    say: \"For a new credential, use {{ call }} and carry its returned reference.\"\n" +
			"    otherwise: \"If you need a new credential, report that need to the coordinating session for protected generation.\"\n"
		identicalSiblingWithArgs := "  - tool: \"secret_generate\"\n" +
			"    args:\n" +
			"      format: \"base64url\"\n" +
			"      scope: \"chat\"\n" +
			"    say: \"For a new credential, use {{ call }} and carry its returned reference.\"\n" +
			"    otherwise: \"If you need a new credential, report that need to the coordinating session for protected generation.\"\n"
		mutatedSteps := strings.Replace(origSteps, origStepZeroBlock, suppressedTargetWithArgs, 1) + identicalSiblingWithArgs
		if !strings.Contains(mutatedSteps, "when: \"paintedwolf.listed\"") {
			t.Fatal("failed to construct identical-args same-tool sibling fixture")
		}
		dispEff := buildDisposableSecurityCatalog(t, moduleRoot, mutatedSteps, "")
		dispStock := cloneStockWithCatalog(t, s, dispEff)
		dispLedger, err := interactions.BuildInteractionLedger(dispEff, s.Schemas, s.Names, nil)
		if err != nil {
			t.Fatalf("BuildInteractionLedger(same-tool sibling): %v", err)
		}
		closeExercisedEdgeScopes(t, dispStock, dispLedger, reps, nil)
		valErr := dispLedger.ValidateZeroUnexplainedGaps()
		assertTargetFailedAndSiblingRetained(
			t, dispLedger, valErr,
			targetInstr, sameToolSiblingInst, "secret_generate",
			"disposable_same_tool_identical_args_sibling_retains_own_credit_while_suppressed_target_fails",
			&diagReport,
		)
	})

	t.Run("disposable_unselected_or_alternative_does_not_gain_sibling_credit", func(t *testing.T) {
		primaryWithIdenticalOr := "  - tool: \"secret_generate\"\n" +
			"    args:\n" +
			"      format: \"base64url\"\n" +
			"      scope: \"chat\"\n" +
			"    say: \"For a new credential, use {{ call }} and carry its returned reference.\"\n" +
			"    otherwise: \"If you need a new credential, report that need to the coordinating session for protected generation.\"\n" +
			"    or:\n" +
			"      - tool: \"secret_generate\"\n" +
			"        args:\n" +
			"          format: \"base64url\"\n" +
			"          scope: \"chat\"\n" +
			"        say: \"For a new credential, use {{ call }} and carry its returned reference.\"\n"
		mutatedSteps := strings.Replace(origSteps, origStepZeroBlock, primaryWithIdenticalOr, 1)
		dispEff := buildDisposableSecurityCatalog(t, moduleRoot, mutatedSteps, "")
		dispStock := cloneStockWithCatalog(t, s, dispEff)
		dispLedger, err := interactions.BuildInteractionLedger(dispEff, s.Schemas, s.Names, nil)
		if err != nil {
			t.Fatalf("BuildInteractionLedger(unselected or): %v", err)
		}
		closeExercisedEdgeScopes(t, dispStock, dispLedger, reps, nil)
		valErr := dispLedger.ValidateZeroUnexplainedGaps()
		assertTargetFailedAndSiblingRetained(
			t, dispLedger, valErr,
			unselectedOrInstr, targetInstr, "secret_generate",
			"disposable_unselected_or_alternative_does_not_gain_sibling_credit",
			&diagReport,
		)
	})

	t.Run("disposable_missing_step_text_delivery_loses_credit", func(t *testing.T) {
		mutatedPolicy := strings.Replace(
			origPolicy,
			"{{ paintedwolf.next_steps }}",
			"{% if paintedwolf.listed %}{{ paintedwolf.next_steps }}{% endif %}",
			1,
		)
		if mutatedPolicy == origPolicy {
			t.Fatal("failed to gate paintedwolf.next_steps in CREDENTIAL_LITERAL policy")
		}
		dispEff := buildDisposableSecurityCatalog(t, moduleRoot, "", mutatedPolicy)
		dispStock := cloneStockWithCatalog(t, s, dispEff)
		dispLedger, err := interactions.BuildInteractionLedger(dispEff, s.Schemas, s.Names, nil)
		if err != nil {
			t.Fatalf("BuildInteractionLedger(missing step text): %v", err)
		}
		closeExercisedEdgeScopes(t, dispStock, dispLedger, reps, nil)
		valErr := dispLedger.ValidateZeroUnexplainedGaps()
		assertTargetFailedAndSiblingRetained(
			t, dispLedger, valErr,
			targetInstr, weakSiblingInstr, "secret_generate",
			"disposable_missing_step_text_delivery_loses_credit",
			&diagReport,
		)
	})

	if outDir := strings.TrimSpace(os.Getenv("PW_INTERACTION_LEDGER_DIR")); outDir != "" {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", outDir, err)
		}
		diagPath := filepath.Join(outDir, "negative-control-diagnostic.txt")
		if err := os.WriteFile(diagPath, []byte(diagReport.String()), 0o644); err != nil {
			t.Fatalf("write %s: %v", diagPath, err)
		}
	}
}

func assertTargetFailedAndSiblingRetained(
	t *testing.T,
	ledger *interactions.InteractionLedger,
	valErr error,
	targetInstr, siblingInstr, siblingAction, label string,
	diag *strings.Builder,
) {
	t.Helper()
	if valErr == nil {
		t.Fatalf("%s: expected ValidateZeroUnexplainedGaps to fail when target step %s is unverified", label, targetInstr)
	}
	var targetOpen []interactions.EdgeScope
	var siblingOpen []interactions.EdgeScope
	for _, gap := range ledger.OpenEdgeGaps {
		if gap.InstructionID == targetInstr && gap.Recipient == "coordinator" {
			targetOpen = append(targetOpen, gap)
		}
		if gap.InstructionID == siblingInstr && gap.Recipient == "coordinator" {
			siblingOpen = append(siblingOpen, gap)
		}
	}
	if len(targetOpen) == 0 {
		t.Fatalf("%s: expected target %s (coordinator) in OpenEdgeGaps, got gaps=%+v", label, targetInstr, ledger.OpenEdgeGaps)
	}
	if len(siblingOpen) > 0 {
		t.Fatalf("%s: expected sibling %s (coordinator) to retain its closed evidence, but found open gaps=%+v", label, siblingInstr, siblingOpen)
	}
	for _, ex := range ledger.ExecutedEdgeAssertions {
		if ex.InstructionID == targetInstr && ex.Recipient == "coordinator" && ex.CheckedOutcome == "emitted" {
			t.Fatalf("%s: target %s unexpectedly received emitted credit: %+v", label, targetInstr, ex)
		}
	}
	var siblingClosed []interactions.ExecutedEdgeAssertion
	for _, ex := range ledger.ExecutedEdgeAssertions {
		if ex.InstructionID == siblingInstr && ex.Recipient == "coordinator" && ex.AssertedAction == siblingAction && ex.CheckedOutcome == "emitted" {
			siblingClosed = append(siblingClosed, ex)
		}
	}
	if len(siblingClosed) == 0 {
		t.Fatalf("%s: expected sibling %s to retain ExecutedEdgeAssertions for %s on coordinator", label, siblingInstr, siblingAction)
	}

	fmt.Fprintf(diag, "[%s]\n", label)
	fmt.Fprintf(diag, "validation_error: %s\n", valErr.Error())
	for _, g := range targetOpen {
		fmt.Fprintf(diag, "open_target_gap: edge=%s instruction=%s recipient=%s action=%s\n", g.EdgeID, g.InstructionID, g.Recipient, g.AffectedAction)
	}
	for _, s := range siblingClosed {
		fmt.Fprintf(diag, "retained_sibling_evidence: edge=%s instruction=%s recipient=%s action=%s outcome=%s input=%s\n",
			s.EdgeID, s.InstructionID, s.Recipient, s.AssertedAction, s.CheckedOutcome, s.InputState)
	}
	diag.WriteString("\n")
}

func buildDisposableSecurityCatalog(t *testing.T, moduleRoot, mutatedSteps, mutatedPolicy string) *extpacks.EffectiveCatalog {
	t.Helper()
	srcSec := filepath.Join(moduleRoot, "config", "packs", "painted-wolf", "security")
	dstSec := filepath.Join(t.TempDir(), "security-disposable")
	err := filepath.WalkDir(srcSec, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, rErr := filepath.Rel(srcSec, path)
		if rErr != nil {
			return rErr
		}
		target := filepath.Join(dstSec, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, raw, 0o644)
	})
	if err != nil {
		t.Fatalf("copy security pack: %v", err)
	}
	if mutatedSteps != "" {
		stepFile := filepath.Join(dstSec, "host", "next-steps", "CREDENTIAL_LITERAL.yaml")
		if err := os.WriteFile(stepFile, []byte(mutatedSteps), 0o644); err != nil {
			t.Fatalf("write mutated next-steps CREDENTIAL_LITERAL.yaml: %v", err)
		}
	}
	if mutatedPolicy != "" {
		polFile := filepath.Join(dstSec, "policy", "CREDENTIAL_LITERAL.yaml")
		if err := os.WriteFile(polFile, []byte(mutatedPolicy), 0o644); err != nil {
			t.Fatalf("write mutated policy CREDENTIAL_LITERAL.yaml: %v", err)
		}
	}

	stockContent, err := extpacks.DiscoverStockContent()
	if err != nil {
		t.Fatalf("DiscoverStockContent: %v", err)
	}
	found := false
	for i, pc := range stockContent {
		if pc.Pack.ID == "painted-wolf/security" {
			mutPC, invErr := extpacks.InventoryPack(extpacks.Pack{ID: pc.Pack.ID, Root: extpacks.OnDisk(dstSec)}, pc.Manifest)
			if invErr != nil {
				t.Fatalf("InventoryPack(security-disposable): %v", invErr)
			}
			mutPC.Kind = pc.Kind
			stockContent[i] = mutPC
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing painted-wolf/security in DiscoverStockContent")
	}
	return extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: stockContent})
}
