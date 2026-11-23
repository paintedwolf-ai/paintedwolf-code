package promptrender_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

type outputReaderRoute string

const (
	routeCallable    outputReaderRoute = "callable"
	routeLoadable    outputReaderRoute = "loadable"
	routeUnavailable outputReaderRoute = "unavailable"
)

type jobOccKind string

const (
	occRunning    jobOccKind = "running"
	occExitedOnly jobOccKind = "exited_only"
	occNoJob      jobOccKind = "no_job"
)

type jobWaitAuthorityRep struct {
	label         string
	isWorker      bool
	coordState    hoststate.CoordinatorState
	workerState   hoststate.WorkerState
	auth          hoststate.TurnAuthority
	overrideMetas []tools.ToolMeta
	route         outputReaderRoute
	waitAdmitted  bool
}

func classifyOutputReaderRoute(auth hoststate.TurnAuthority) outputReaderRoute {
	if slices.Contains(auth.Offered, "command_output") {
		return routeCallable
	}
	if slices.Contains(auth.Reader.Requestable(), "command_output") && slices.Contains(auth.Offered, "request_tools") {
		return routeLoadable
	}
	return routeUnavailable
}

func jobSnapshotsForOccurrence(occ jobOccKind) []bgprocess.JobSnapshot {
	now := time.Now().UTC()
	switch occ {
	case occRunning:
		return []bgprocess.JobSnapshot{{
			Handle: "cmd-7", Mode: bgprocess.JobModeBackground, OriginTool: "command",
			StartedAt: now.Add(-5 * time.Second),
		}}
	case occExitedOnly:
		return []bgprocess.JobSnapshot{{
			Handle: "cmd-8", Mode: bgprocess.JobModeBackground, OriginTool: "command",
			StartedAt: now.Add(-12 * time.Second), Exited: true, ExitCode: 2, Refusals: 1,
		}}
	default:
		return nil
	}
}

func renderJobGuidanceForRep(t *testing.T, s *promptrender.Stock, rep jobWaitAuthorityRep, jobs []bgprocess.JobSnapshot) []string {
	t.Helper()
	if rep.isWorker {
		block := inject.RenderCommandJobsBlock(
			t.Context(), prompts.NewInjectRenderer(s.Engine()),
			rep.auth.Reader, rep.auth.Schemas["wait"],
			"sess-worker-job", jobs, nil, time.Now().UTC(), "worker:"+rep.workerState.AgentID,
		)
		if strings.TrimSpace(block) == "" {
			return nil
		}
		return []string{block}
	}
	wsDir := ""
	if rep.coordState.RootCount > 0 {
		wsDir = t.TempDir()
	}
	cs := rep.coordState
	overlay := cs.Overlay
	if overlay.Catalog == nil {
		overlays, err := hoststate.DiscoverPackOverlays(t.Context())
		if err != nil {
			t.Fatalf("DiscoverPackOverlays: %v", err)
		}
		overlay = overlays[0]
	}
	exec, narrowed, err := hoststate.ExecutorForOverlay(
		overlay, cs.Posture, false, cs.WebSearch, cs.CommandExternalHidden, cs.MCPState,
	)
	if err != nil {
		t.Fatalf("ExecutorForOverlay(%s): %v", rep.label, err)
	}
	effCat := s.Catalog
	effSchemas := s.Schemas
	if overlay.Catalog != nil {
		effCat = overlay.Catalog
	}
	if overlay.Schemas != nil {
		effSchemas = overlay.Schemas
	}
	view := &catalogview.View{
		Catalog:      effCat,
		ToolProfiles: narrowed,
		ToolSchemas:  effSchemas,
	}
	sess := &api.Session{
		ID: "sess-coord-3way", ProjectID: "proj-3way", WorkspacePath: wsDir,
		AgentType: prompts.CoordinatorProfileID,
		Posture:   cs.Posture, Mode: cs.SessionMode,
	}
	history := []api.Message{{ID: "u1", Role: api.MessageRoleUser, Content: "Check current progress."}}
	runCtx := hoststate.NormalizeCoordinatorRunContext(cs, sess, history)
	pe := s.Engine()
	eng := &assembly.AssemblyEngine{}
	eng.SetDeps(assembly.AssemblyDeps{
		Prompts:          pe,
		Injects:          prompts.NewInjectRenderer(pe),
		SessionView:      func(context.Context, *api.Session) *catalogview.View { return view },
		Selection:        staticSelection{loaded: cs.Loaded},
		Limits:           func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		CoordinatorFrame: staticCoordFrame{ctx: runCtx},
		ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
			return cs.ImplementState
		},
		EffectivePromptSurface: func(context.Context, *api.Session) prompts.AgentPromptSurface {
			return prompts.AgentPromptSurface{
				Posture:               cs.ApprovalPosture,
				CommandExternalHidden: cs.CommandExternalHidden,
				SessionScratchDir:     cs.SessionScratchDir,
				SharedTempLocations:   cs.SharedTempLocations,
			}
		},
		PromptToolLister: func(ctx context.Context, _ *api.Session, profileID string) ([]tools.ToolMeta, error) {
			if len(rep.overrideMetas) > 0 {
				return slices.Clone(rep.overrideMetas), nil
			}
			return exec.Metadata.List(ctx, platform.ToolFilter{
				ProfileID:        profileID,
				ProjectRootCount: cs.RootCount,
			})
		},
		WebSearchEnabled: func() bool { return cs.WebSearch },
		WorkspaceRoots: func(_ context.Context, s2 *api.Session) ([]map[string]any, int, string, int) {
			if s2 == nil || cs.RootCount == 0 || strings.TrimSpace(s2.WorkspacePath) == "" {
				return nil, 0, "", 0
			}
			return ensureWorkspaceTopologyDirs(s2.WorkspacePath, cs.RootCount, cs.HasGit, cs.HasAnyGitRoot)
		},
		CommandJobs: func(string) []bgprocess.JobSnapshot { return slices.Clone(jobs) },
	})
	msgs, err := eng.BuildCompletionMessages(t.Context(), sess, history, nil)
	if err != nil {
		t.Fatalf("BuildCompletionMessages(%s): %v", rep.label, err)
	}
	return newlyDeliveredHostInstructions(msgs, "")
}

// verifyIndependentJobWaitExpectedActions derives expected running, exited-only,
// and no-job actions strictly from structured jobs + turn offer/requestable/schemas
// (held = absent) and checks direct read, loader advice, and process_done wait separately.
func verifyIndependentJobWaitExpectedActions(
	cat *toolvocab.Catalog,
	hostBlocks []string,
	jobs []bgprocess.JobSnapshot,
	auth hoststate.TurnAuthority,
) error {
	hasRunning, hasExited := false, false
	for _, j := range jobs {
		if j.Exited {
			hasExited = true
		} else {
			hasRunning = true
		}
	}
	route := classifyOutputReaderRoute(auth)
	waitAdmitted := slices.Contains(auth.Offered, "wait") && inject.WaitSchemaAllowsProcessDone(auth.Schemas["wait"])

	joined := strings.Join(hostBlocks, "\n")
	hasLiveHandles := strings.Contains(joined, "## Live handles")
	directCalls := parsedCallsForTool(cat, hostBlocks, "command_output")
	loaderCalls := parsedCallsForTool(cat, hostBlocks, "request_tools")
	hasOutputLoader := slices.ContainsFunc(loaderCalls, func(ex toolvocabtest.CallExample) bool {
		return fmt.Sprint(ex.Args["need"]) == "command_output"
	})
	waitCalls := parsedCallsForTool(cat, hostBlocks, "wait")
	hasWaitProcessDone := slices.ContainsFunc(waitCalls, func(ex toolvocabtest.CallExample) bool {
		return strings.Contains(ex.Raw, "process_done")
	})

	if !hasRunning && !hasExited {
		if hasLiveHandles || len(directCalls) > 0 || hasOutputLoader || hasWaitProcessDone {
			return fmt.Errorf("no_job emitted unsolicited live-job instruction (section=%t, direct=%d, loader=%t, wait=%t)",
				hasLiveHandles, len(directCalls), hasOutputLoader, hasWaitProcessDone)
		}
		return nil
	}

	if !hasLiveHandles {
		return fmt.Errorf("expected ## Live handles section when jobs=%d", len(jobs))
	}

	if hasRunning {
		wantDirect := route == routeCallable
		wantLoader := route == routeLoadable
		wantWait := waitAdmitted
		if (len(directCalls) > 0) != wantDirect {
			return fmt.Errorf("running job direct command_output(...) call=%t, want %t (route=%s)", len(directCalls) > 0, wantDirect, route)
		}
		if hasOutputLoader != wantLoader {
			return fmt.Errorf("running job request_tools(need=\"command_output\")=%t, want %t (route=%s)", hasOutputLoader, wantLoader, route)
		}
		if hasWaitProcessDone != wantWait {
			return fmt.Errorf("running job wait(process_done)=%t, want %t (waitAdmitted=%t)", hasWaitProcessDone, wantWait, waitAdmitted)
		}
		return nil
	}

	// Exited-only case checked on its own terms: terminal state row, no process_done wait, route-matched read advice.
	if !strings.Contains(joined, "state=exited") || !strings.Contains(joined, "terminal state") {
		return fmt.Errorf("exited_only job missing terminal state ledger row: %q", joined)
	}
	if hasWaitProcessDone {
		return fmt.Errorf("exited_only job unexpectedly recommended wait(process_done): %q", joined)
	}
	if len(directCalls) > 0 || strings.Contains(joined, "Do not relaunch a live job.") {
		return fmt.Errorf("exited_only job leaked running-job live instruction: %q", joined)
	}
	mentionsDirectTool := slices.Contains(toolvocab.ToolsIn(cat, extractSection(joined, "## Live handles")), "command_output") && !hasOutputLoader
	switch route {
	case routeCallable:
		if !mentionsDirectTool || hasOutputLoader {
			return fmt.Errorf("exited_only + callable expected direct command_output advice (got direct=%t loader=%t)", mentionsDirectTool, hasOutputLoader)
		}
	case routeLoadable:
		if !hasOutputLoader {
			return fmt.Errorf("exited_only + loadable expected request_tools(need=\"command_output\")")
		}
	case routeUnavailable:
		if mentionsDirectTool || hasOutputLoader {
			return fmt.Errorf("exited_only + unavailable leaked command_output or loader action")
		}
	}
	return nil
}

func extractSection(text, heading string) string {
	idx := strings.Index(text, heading)
	if idx < 0 {
		return ""
	}
	rest := text[idx:]
	if next := strings.Index(rest[len(heading):], "\n## "); next >= 0 {
		return rest[:len(heading)+next]
	}
	return rest
}

// TestLiveJobThreeWayInteractionMatrixAndLedger executes the 3-way
// (job occurrence × output-reader route × wait process_done admission) matrix,
// verifies available read survives narrowed wait, builds the source-derived AST+SSA
// interaction ledger, asserts 0 unexplained gaps, and writes ledger artifacts.
func TestLiveJobThreeWayInteractionMatrixAndLedger(t *testing.T) {
	s := mustStock(t)
	coordStates, err := hoststate.CoordinatorStates(t.Context())
	if err != nil {
		t.Fatalf("CoordinatorStates: %v", err)
	}
	workerStates, err := hoststate.WorkerStates(t.Context())
	if err != nil {
		t.Fatalf("WorkerStates: %v", err)
	}

	// Dynamically discover supported (route, waitAdmitted) authority shapes across stock states, deduplicating projections.
	reps := map[string]jobWaitAuthorityRep{}
	stateVisits := 0
	for _, cs := range coordStates {
		if cs.Excluded || cs.PackOverlayID != "stock" {
			continue
		}
		stateVisits++
		auth, fErr := hoststate.FreezeTurnAuthorityWithResources(t.Context(), cs, toolcontract.ResourcePresence{CommandJobs: true})
		if fErr != nil {
			t.Fatalf("FreezeTurnAuthorityWithResources(%s): %v", cs.ID, fErr)
		}
		rt := classifyOutputReaderRoute(auth)
		wa := slices.Contains(auth.Offered, "wait") && inject.WaitSchemaAllowsProcessDone(auth.Schemas["wait"])
		key := fmt.Sprintf("route=%s|wait_process_done=%t", rt, wa)
		if _, exists := reps[key]; !exists {
			reps[key] = jobWaitAuthorityRep{
				label: cs.ID, coordState: cs, auth: auth, route: rt, waitAdmitted: wa,
			}
		}
	}
	for _, ws := range workerStates {
		if ws.Excluded || ws.Overlay.ID != "stock" {
			continue
		}
		stateVisits++
		auth, fErr := hoststate.FreezeWorkerAuthority(t.Context(), ws)
		if fErr != nil {
			t.Fatalf("FreezeWorkerAuthority(%s): %v", ws.ID, fErr)
		}
		rt := classifyOutputReaderRoute(auth)
		wa := slices.Contains(auth.Offered, "wait") && inject.WaitSchemaAllowsProcessDone(auth.Schemas["wait"])
		key := fmt.Sprintf("route=%s|wait_process_done=%t", rt, wa)
		if _, exists := reps[key]; !exists {
			reps[key] = jobWaitAuthorityRep{
				label: ws.ID, isWorker: true, workerState: ws, auth: auth, route: rt, waitAdmitted: wa,
			}
		}
		if ws.AgentID == "implementer" && ws.Loading == hoststate.WorkerLoadingStickyOnly {
			if _, hasLoadableWait := reps["route=loadable|wait_process_done=true"]; !hasLoadableWait {
				wsWaitLoaded := ws
				wsWaitLoaded.ID = ws.ID + "+loaded=wait"
				wsWaitLoaded.Loaded = map[string]bool{"wait": true}
				if wAuth, wErr := hoststate.FreezeWorkerAuthority(t.Context(), wsWaitLoaded); wErr == nil {
					wRt := classifyOutputReaderRoute(wAuth)
					wWa := slices.Contains(wAuth.Offered, "wait") && inject.WaitSchemaAllowsProcessDone(wAuth.Schemas["wait"])
					if wRt == routeLoadable && wWa {
						stateVisits++
						reps["route=loadable|wait_process_done=true"] = jobWaitAuthorityRep{
							label: wsWaitLoaded.ID, isWorker: true, workerState: wsWaitLoaded, auth: wAuth, route: wRt, waitAdmitted: wWa,
						}
					}
				}
			}
		}
	}

	var realizableCells, assertedCells []string
	var exclusions []interactions.WitnessCellExclusion
	allRoutes := []outputReaderRoute{routeCallable, routeLoadable, routeUnavailable}
	allWaits := []bool{true, false}
	allOccs := []jobOccKind{occRunning, occExitedOnly, occNoJob}

	for _, rt := range allRoutes {
		for _, wa := range allWaits {
			key := fmt.Sprintf("route=%s|wait_process_done=%t", rt, wa)
			rep, ok := reps[key]
			for _, occ := range allOccs {
				tuple := fmt.Sprintf("occ=%s|%s", occ, key)
				if !ok {
					exclusions = append(exclusions, interactions.WitnessCellExclusion{
						Tuple:            tuple,
						ProductionReason: "no stock coordinator surface or worker tool profile realizes " + key + " simultaneously under coordinator-surfaces.yaml + tool-profiles.yaml",
					})
					continue
				}
				realizableCells = append(realizableCells, tuple)
				jobs := jobSnapshotsForOccurrence(occ)
				blocks := renderJobGuidanceForRep(t, s, rep, jobs)
				if err := verifyIndependentJobWaitExpectedActions(s.Names, blocks, jobs, rep.auth); err != nil {
					t.Fatalf("3-way cell %s (%s) failed: %v", tuple, rep.label, err)
				}
				assertedCells = append(assertedCells, tuple)
			}
		}
	}

	// Explicit narrowed-wait survival witness: keep wait offered (http_ready, port_ready)
	// while narrowing out process_done on a callable command_output turn.
	callableRep, hasCallableTrue := reps["route=callable|wait_process_done=true"]
	if !hasCallableTrue {
		t.Fatal("expected stock state with route=callable|wait_process_done=true")
	}
	narrowedAuth := narrowWaitSchemaExcludeProcessDone(callableRep.auth)
	narrowRep := callableRep
	narrowRep.label = callableRep.label + "/narrowed-wait-schema"
	narrowRep.auth = narrowedAuth
	narrowRep.overrideMetas = narrowedAuth.Metas
	narrowRep.waitAdmitted = false
	narrowBlocks := renderJobGuidanceForRep(t, s, narrowRep, jobSnapshotsForOccurrence(occRunning))
	if err := verifyIndependentJobWaitExpectedActions(s.Names, narrowBlocks, jobSnapshotsForOccurrence(occRunning), narrowedAuth); err != nil {
		t.Fatalf("available command_output read did not survive narrowed wait schema: %v", err)
	}
	if len(parsedCallsForTool(s.Names, narrowBlocks, "command_output")) == 0 {
		t.Fatal("expected direct command_output(...) call to survive when wait schema narrows out process_done")
	}

	// Build full source-derived AST + SSA interaction ledger and populate witness rows + executed edge assertions.
	ledger, err := interactions.BuildInteractionLedger(s.Catalog, s.Schemas, s.Names, nil)
	if err != nil {
		t.Fatalf("BuildInteractionLedger: %v", err)
	}
	if ledger.TotalDiscoveredSites < 100 {
		t.Fatalf("expected >= 100 discovered actionable sites across stock catalog, got %d", ledger.TotalDiscoveredSites)
	}
	if ledger.ProducerGraphStats.DefaultGraphBuildCount != 1 || ledger.ProducerGraphStats.IndexedFunctionCount == 0 {
		t.Fatalf("unexpected ProducerGraphStats: %+v", ledger.ProducerGraphStats)
	}

	closeExercisedEdgeScopes(t, s, ledger, reps, nil)

	var jobTrace, archiveTrace interactions.CompactInstructionTrace
	for _, ct := range ledger.CompactTraces {
		switch ct.Name {
		case "job-wait":
			jobTrace = ct
		case "archive-recovery":
			archiveTrace = ct
		}
	}
	if len(jobTrace.CapabilityFacts) == 0 || len(archiveTrace.CapabilityFacts) == 0 {
		t.Fatalf("expected non-empty compact traces for job-wait (%d) and archive-recovery (%d)",
			len(jobTrace.CapabilityFacts), len(archiveTrace.CapabilityFacts))
	}

	ledger.AddWitnessRow(interactions.WitnessLedgerRow{
		WitnessID:               "live-job-3way-matrix",
		InstructionID:           "guidance:command-jobs|guidance/command-jobs:L8-L11",
		SourceSpan:              "guidance/command-jobs:L8-L11",
		AffectedAction:          "command_output / request_tools(need=command_output) / wait(process_done)",
		JointCapabilityFacts:    namesFromTraces(jobTrace.CapabilityFacts),
		JointOccurrenceFacts:    namesFromTraces(jobTrace.OccurrenceFacts),
		DerivedPrimitiveInputs:  primitivesFromTraces(jobTrace.CapabilityFacts, jobTrace.OccurrenceFacts),
		StateVisits:             stateVisits,
		DeduplicatedProjections: len(reps),
		RealizableRelevantCells: realizableCells,
		IndependentlyAsserted:   assertedCells,
		Exclusions:              exclusions,
	})

	archiveCells := []string{
		"occ=extract_archive_rejected|route=loadable_no_schema",
		"occ=extract_archive_rejected|route=unavailable",
		"occ=unrelated_reject|route=loadable_no_schema",
		"occ=unrelated_reject|route=unavailable",
	}
	ledger.AddWitnessRow(interactions.WitnessLedgerRow{
		WitnessID:               "archive-recovery-2x2-relevance",
		InstructionID:           "oar:tool.rejected:EXTRACT_FORMAT_UNSUPPORTED|policy/EXTRACT_FORMAT_UNSUPPORTED#fix",
		SourceSpan:              "policy/EXTRACT_FORMAT_UNSUPPORTED#fix:L1",
		AffectedAction:          "request_tools(need=command) / archive_unzip_stream_call",
		JointCapabilityFacts:    namesFromTraces(archiveTrace.CapabilityFacts),
		JointOccurrenceFacts:    namesFromTraces(archiveTrace.OccurrenceFacts),
		DerivedPrimitiveInputs:  primitivesFromTraces(archiveTrace.CapabilityFacts, archiveTrace.OccurrenceFacts),
		StateVisits:             4,
		DeduplicatedProjections: 2,
		RealizableRelevantCells: archiveCells,
		IndependentlyAsserted:   slices.Clone(archiveCells),
	})

	if outDir := strings.TrimSpace(os.Getenv("PW_INTERACTION_LEDGER_DIR")); outDir != "" {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", outDir, err)
		}
		jsonBytes, err := ledger.MarshalPrettyJSON()
		if err != nil {
			t.Fatalf("MarshalPrettyJSON: %v", err)
		}
		if err := os.WriteFile(filepath.Join(outDir, "interaction-ledger.json"), jsonBytes, 0o644); err != nil {
			t.Fatalf("write interaction-ledger.json: %v", err)
		}
		if err := os.WriteFile(filepath.Join(outDir, "interaction-ledger.md"), []byte(ledger.RenderMarkdown()), 0o644); err != nil {
			t.Fatalf("write interaction-ledger.md: %v", err)
		}
	}

	if err := ledger.ValidateZeroUnexplainedGaps(); err != nil {
		t.Fatalf("ValidateZeroUnexplainedGaps: %v", err)
	}

	// Removing an executed assertion or changing its recipient must fail on that exact edge.
	t.Run("lost_witness_negative_control", func(t *testing.T) {
		if len(ledger.RequiredEdgeScopes) == 0 || len(ledger.ExecutedEdgeAssertions) == 0 {
			t.Fatal("expected at least 1 ExecutedEdgeAssertion closing a real unresolved edge")
		}
		target := ledger.RequiredEdgeScopes[0]
		matchesTarget := func(a interactions.ExecutedEdgeAssertion) bool {
			return a.EdgeID == target.EdgeID && a.InstructionID == target.InstructionID &&
				a.Recipient == target.Recipient && a.AssertedAction == target.AffectedAction
		}

		removed := ledger.Clone()
		removed.ExecutedEdgeAssertions = slices.DeleteFunc(removed.ExecutedEdgeAssertions, matchesTarget)
		remErr := removed.ValidateZeroUnexplainedGaps()
		if remErr == nil || !strings.Contains(remErr.Error(), target.EdgeID) || !strings.Contains(remErr.Error(), target.InstructionID) {
			t.Fatalf("expected removing assertions for %s to fail naming edge and instruction, got: %v", target.EdgeID, remErr)
		}

		retargeted := ledger.Clone()
		for i := range retargeted.ExecutedEdgeAssertions {
			if matchesTarget(retargeted.ExecutedEdgeAssertions[i]) {
				retargeted.ExecutedEdgeAssertions[i].Recipient = target.Recipient + "_mismatched"
			}
		}
		retErr := retargeted.ValidateZeroUnexplainedGaps()
		if retErr == nil || !strings.Contains(retErr.Error(), target.EdgeID) || !strings.Contains(retErr.Error(), "recipient="+target.Recipient) {
			t.Fatalf("expected changing assertion recipient from %s to fail naming edge and original recipient, got: %v", target.Recipient, retErr)
		}
	})
}

func narrowWaitSchemaExcludeProcessDone(auth hoststate.TurnAuthority) hoststate.TurnAuthority {
	narrowedWait := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"until_complete": map[string]any{"type": "boolean"},
			"conditions": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"kind": map[string]any{"type": "string", "enum": []any{"http_ready", "port_ready"}},
						"url":  map[string]any{"type": "string"},
					},
				},
			},
		},
	}
	schemas := make(map[string]map[string]any, len(auth.Schemas))
	for k, v := range auth.Schemas {
		schemas[k] = v
	}
	schemas["wait"] = narrowedWait
	metas := make([]tools.ToolMeta, len(auth.Metas))
	for i, m := range auth.Metas {
		metas[i] = m
		if m.Name == "wait" {
			metas[i].ArgsSchema = narrowedWait
		}
	}
	out := auth
	out.Schemas = schemas
	out.Metas = metas
	out.Reach = auth.Reach.WithSchemas(schemas)
	return out
}

func namesFromTraces(traces []interactions.FactTrace) []string {
	var out []string
	for _, tr := range traces {
		out = append(out, tr.Fact)
	}
	return out
}

func primitivesFromTraces(groups ...[]interactions.FactTrace) []string {
	set := map[string]bool{}
	for _, g := range groups {
		for _, tr := range g {
			for _, p := range tr.PrimitiveInputs {
				set[p] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// TestMaskedTwoAxisInteractionAndMissingGuardFaults verifies the masked
// two-axis coincidence fault (Ask/Agent × primary-root Git/non-Git) and the
// missing-guard relevance obligation variant on the same fixture.
func TestMaskedTwoAxisInteractionAndMissingGuardFaults(t *testing.T) {
	s := mustStock(t)
	coordStates, err := hoststate.CoordinatorStates(t.Context())
	if err != nil {
		t.Fatalf("CoordinatorStates: %v", err)
	}

	rect, maskPred := selectTwoAxisPreservingRectangle(t, coordStates)
	runningJobs := jobSnapshotsForOccurrence(occRunning)

	origRaw, _, ok := s.Catalog.UnitContent("guidance/command-jobs")
	if !ok {
		t.Fatal("missing stock guidance/command-jobs")
	}

	// Suppress command_output(...) only when both changed facts coincide.
	maskedJobs := strings.Replace(
		string(origRaw),
		"{% if profile_has_command_output %}",
		"{% if profile_has_command_output and not ("+maskPred+") %}",
		1,
	)
	maskedEff := buildDisposablePackCatalog(t, "acme/masked-2axis", map[string]string{
		"guidance/command-jobs.md": maskedJobs,
	}, map[string]string{"guidance/command-jobs": "acme/masked-2axis"})
	maskedStock := cloneStockWithCatalog(t, s, maskedEff)

	checkCell := func(targetStock *promptrender.Stock, st hoststate.CoordinatorState) error {
		auth, fErr := hoststate.FreezeTurnAuthorityWithResources(t.Context(), st, toolcontract.ResourcePresence{CommandJobs: true})
		if fErr != nil {
			return fErr
		}
		blocks := renderJobGuidanceForRep(t, targetStock, jobWaitAuthorityRep{
			label: st.ID, coordState: st, auth: auth,
		}, runningJobs)
		if err := verifyIndependentJobWaitExpectedActions(s.Names, blocks, runningJobs, auth); err != nil {
			return fmt.Errorf("state=%s instruction=command_output: %w", st.ID, err)
		}
		return nil
	}

	// Baseline and single-axis neighbors remain unaffected by the two-axis mask.
	for _, name := range []string{"baseline_agent_git", "neighbor_ask_git", "neighbor_agent_nongit"} {
		if err := checkCell(maskedStock, rect[name]); err != nil {
			t.Fatalf("expected %s (%s) to pass under masked 2-axis fault, got: %v", name, rect[name].ID, err)
		}
	}
	// The combined two-axis cell fails naming instruction and state.
	combErr := checkCell(maskedStock, rect["combined_ask_nongit"])
	if combErr == nil || !strings.Contains(combErr.Error(), rect["combined_ask_nongit"].ID) || !strings.Contains(combErr.Error(), "command_output") {
		t.Fatalf("expected combined 2-axis cell %s to fail naming state and command_output, got: %v", rect["combined_ask_nongit"].ID, combErr)
	}
	for _, name := range []string{"baseline_agent_git", "neighbor_ask_git", "neighbor_agent_nongit", "combined_ask_nongit"} {
		if err := checkCell(s, rect[name]); err != nil {
			t.Fatalf("expected restored stock copy to pass %s (%s), got: %v", name, rect[name].ID, err)
		}
	}

	// Erase {% if has_running %} while keeping schema-valid command_output(...).
	missingGuardJobs := strings.Replace(string(origRaw), "{% if has_running %}", "{% if true %}", 1)
	mgEff := buildDisposablePackCatalog(t, "acme/missing-guard", map[string]string{
		"guidance/command-jobs.md": missingGuardJobs,
	}, map[string]string{"guidance/command-jobs": "acme/missing-guard"})
	mgStock := cloneStockWithCatalog(t, s, mgEff)

	baseSt := rect["baseline_agent_git"]
	baseAuth, _ := hoststate.FreezeTurnAuthorityWithResources(t.Context(), baseSt, toolcontract.ResourcePresence{CommandJobs: true})
	exitedBlocks := renderJobGuidanceForRep(t, mgStock, jobWaitAuthorityRep{
		label: baseSt.ID, coordState: baseSt, auth: baseAuth,
	}, jobSnapshotsForOccurrence(occExitedOnly))

	// Negative schema/offer checks still pass, while relevance checks reject the ungrounded call.
	for _, blk := range exitedBlocks {
		if viols := promptrender.EvaluateClaimsForTest(s, "guidance/command-jobs ("+baseSt.ID+")", blk, baseAuth, baseSt.SessionMode, baseSt.Posture, toolvocabtest.SubjectSelfNow(), true); len(viols) != 0 {
			t.Fatalf("expected missing-guard call to remain schema-valid, got violations: %v", viols)
		}
	}
	mgErr := verifyIndependentJobWaitExpectedActions(s.Names, exitedBlocks, jobSnapshotsForOccurrence(occExitedOnly), baseAuth)
	if mgErr == nil {
		t.Fatalf("expected recipient-level relevance obligation to fail when has_running guard is erased on exited_only: %q", strings.Join(exitedBlocks, "\n"))
	}
}

func cloneStockWithCatalog(t *testing.T, base *promptrender.Stock, eff *extpacks.EffectiveCatalog) *promptrender.Stock {
	t.Helper()
	cp := *base
	cp.Catalog = eff
	return &cp
}

func selectTwoAxisPreservingRectangle(t *testing.T, coordStates []hoststate.CoordinatorState) (map[string]hoststate.CoordinatorState, string) {
	t.Helper()
	findCell := func(mode api.SessionMode, primaryGit bool) (hoststate.CoordinatorState, bool) {
		wantRoot := hoststate.RootsSingleNonGit
		if primaryGit {
			wantRoot = hoststate.RootsSingleGit
		}
		for _, c := range coordStates {
			if c.Excluded || c.PackOverlayID != "stock" || c.Posture != api.SessionPostureBuild ||
				c.WorkflowState != hoststate.WorkflowAmbientIdle || c.Loading != hoststate.LoadingAllLoaded {
				continue
			}
			if c.SessionMode != mode || c.RootTopology != wantRoot {
				continue
			}
			auth, err := hoststate.FreezeTurnAuthorityWithResources(t.Context(), c, toolcontract.ResourcePresence{CommandJobs: true})
			if err == nil && classifyOutputReaderRoute(auth) == routeCallable {
				return c, true
			}
		}
		return hoststate.CoordinatorState{}, false
	}

	b, ok1 := findCell(api.SessionModeAgent, true)
	n1, ok2 := findCell(api.SessionModeAsk, true)
	n2, ok3 := findCell(api.SessionModeAgent, false)
	comb, ok4 := findCell(api.SessionModeAsk, false)
	if ok1 && ok2 && ok3 && ok4 {
		return map[string]hoststate.CoordinatorState{
			"baseline_agent_git":    b,
			"neighbor_ask_git":      n1,
			"neighbor_agent_nongit": n2,
			"combined_ask_nongit":   comb,
		}, `session_mode == "ask" and not has_git`
	}

	// Lexical fallback across observed (axisID, value) pairs preserving callable command_output route.
	type axisExtractor struct {
		id       string
		maskTerm func(val string) string
		val      func(hoststate.CoordinatorState, hoststate.TurnAuthority) string
	}
	axes := []axisExtractor{
		{
			id: "posture_mutation",
			maskTerm: func(v string) string {
				return map[string]string{"false": "not profile_command_mutation", "true": "profile_command_mutation"}[v]
			},
			val: func(_ hoststate.CoordinatorState, a hoststate.TurnAuthority) string {
				return fmt.Sprintf("%t", a.Reader.Facts()["profile_command_mutation"] == true)
			},
		},
		{
			id:       "primary_git",
			maskTerm: func(v string) string { return map[string]string{"false": "not has_git", "true": "has_git"}[v] },
			val: func(c hoststate.CoordinatorState, _ hoststate.TurnAuthority) string {
				return fmt.Sprintf("%t", c.HasGit)
			},
		},
		{
			id:       "session_mode",
			maskTerm: func(v string) string { return fmt.Sprintf("session_mode == %q", v) },
			val:      func(c hoststate.CoordinatorState, _ hoststate.TurnAuthority) string { return string(c.SessionMode) },
		},
	}
	type eligiblePair struct {
		st   hoststate.CoordinatorState
		auth hoststate.TurnAuthority
	}
	var eligible []eligiblePair
	for _, c := range coordStates {
		if c.Excluded || c.PackOverlayID != "stock" || c.RootCount != 1 || c.WorkflowState != hoststate.WorkflowAmbientIdle || c.Loading != hoststate.LoadingAllLoaded {
			continue
		}
		if auth, err := hoststate.FreezeTurnAuthorityWithResources(t.Context(), c, toolcontract.ResourcePresence{CommandJobs: true}); err == nil && classifyOutputReaderRoute(auth) == routeCallable {
			eligible = append(eligible, eligiblePair{st: c, auth: auth})
		}
	}
	for i := 0; i < len(axes); i++ {
		for j := i + 1; j < len(axes); j++ {
			fi := func(p eligiblePair) string { return axes[i].val(p.st, p.auth) }
			fj := func(p eligiblePair) string { return axes[j].val(p.st, p.auth) }
			valsI, valsJ := distinctPairAxisValues(eligible, fi), distinctPairAxisValues(eligible, fj)
			if len(valsI) < 2 || len(valsJ) < 2 {
				continue
			}
			// Use ("true", "true") as baseline when boolean so ("false", "false") is the combined non-baseline corner.
			vI0, vI1 := valsI[len(valsI)-1], valsI[0]
			vJ0, vJ1 := valsJ[len(valsJ)-1], valsJ[0]
			c00, ok00 := matchPairTwoAxes(eligible, fi, vI0, fj, vJ0)
			c10, ok10 := matchPairTwoAxes(eligible, fi, vI1, fj, vJ0)
			c01, ok01 := matchPairTwoAxes(eligible, fi, vI0, fj, vJ1)
			c11, ok11 := matchPairTwoAxes(eligible, fi, vI1, fj, vJ1)
			if ok00 && ok10 && ok01 && ok11 {
				return map[string]hoststate.CoordinatorState{
					"baseline_agent_git":    c00,
					"neighbor_ask_git":      c10,
					"neighbor_agent_nongit": c01,
					"combined_ask_nongit":   c11,
				}, axes[i].maskTerm(vI1) + " and " + axes[j].maskTerm(vJ1)
			}
		}
	}
	t.Fatal("fixture qualification failed: no complete two-axis rectangle preserving callable command_output route")
	return nil, ""
}

func distinctPairAxisValues[T any](items []T, fn func(T) string) []string {
	m := map[string]bool{}
	for _, it := range items {
		m[fn(it)] = true
	}
	out := make([]string, 0, len(m))
	for v := range m {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

func matchPairTwoAxes[T interface {
	~struct {
		st   hoststate.CoordinatorState
		auth hoststate.TurnAuthority
	}
}](items []T, f1 func(T) string, v1 string, f2 func(T) string, v2 string) (hoststate.CoordinatorState, bool) {
	for _, it := range items {
		if f1(it) == v1 && f2(it) == v2 {
			return struct {
				st   hoststate.CoordinatorState
				auth hoststate.TurnAuthority
			}(it).st, true
		}
	}
	return hoststate.CoordinatorState{}, false
}

func pickWorkerRepForEdge(t *testing.T, workerStates []hoststate.WorkerState, agentID string, posture api.SessionPosture) jobWaitAuthorityRep {
	t.Helper()
	for _, ws := range workerStates {
		if !ws.Excluded && ws.Overlay.ID == "stock" && ws.AgentID == agentID && ws.Posture == posture && ws.Loading == hoststate.WorkerLoadingAllLoaded {
			wAuth, wErr := hoststate.FreezeWorkerAuthority(t.Context(), ws)
			if wErr == nil {
				return jobWaitAuthorityRep{label: ws.ID, isWorker: true, workerState: ws, auth: wAuth}
			}
		}
	}
	t.Fatalf("missing %s (%s) worker rep for edge assertions", agentID, posture)
	return jobWaitAuthorityRep{}
}
