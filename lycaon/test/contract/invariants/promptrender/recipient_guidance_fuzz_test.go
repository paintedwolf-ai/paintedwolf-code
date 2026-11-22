package promptrender_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

func encodeCoordinatorFuzzBytes(
	d *hoststate.SeedDomains,
	ovID string,
	posture api.SessionPosture,
	mode api.SessionMode,
	surfID string,
	roots hoststate.RootTopology,
	loading hoststate.LoadingMode,
	wf hoststate.WorkflowState,
	app gate.Posture,
	temp hoststate.TempState,
	mcp hoststate.MCPState,
	family, itemSel, numChanges uint8,
	ch1Axis, ch1Val, ch2Axis, ch2Val uint8,
) []byte {
	ovIdx := max(0, slices.IndexFunc(d.Overlays, func(o hoststate.PackOverlay) bool { return o.ID == ovID }))
	pIdx := 0
	for i := range d.Postures {
		if d.Postures[i].ID == posture {
			pIdx = i
			break
		}
	}
	mIdx := max(0, slices.Index(d.SessionModes, mode))
	sIdx := 0
	for i := range d.Surfaces {
		if d.Surfaces[i].ID == surfID {
			sIdx = i
			break
		}
	}
	rIdx := max(0, slices.Index(d.RootTopologies, roots))
	lIdx := max(0, slices.Index(d.LoadingModes, loading))
	wIdx := max(0, slices.Index(d.WorkflowStates, wf))
	aIdx := max(0, slices.Index(d.ApprovalPostures, app))
	tIdx := max(0, slices.Index(d.TempStates, temp))
	mcpIdx := max(0, slices.Index(d.MCPStates, mcp))

	return []byte{
		fuzzDecoderVersion1,
		0, // coordinator
		uint8(ovIdx),
		uint8(pIdx),
		uint8(mIdx),
		uint8(sIdx),
		uint8(rIdx),
		uint8(lIdx),
		uint8(wIdx),
		uint8(aIdx),
		uint8(tIdx),
		uint8(mcpIdx),
		1, // WebSearch=true
		family,
		itemSel,
		numChanges,
		ch1Axis, ch1Val,
		ch2Axis, ch2Val,
	}
}

func encodeWorkerFuzzBytes(
	d *hoststate.SeedDomains,
	ovID, agentID string,
	posture api.SessionPosture,
	parentMode api.SessionMode,
	branch hoststate.WorkerBranchView,
	loading hoststate.WorkerLoadingMode,
	family, itemSel, numChanges, ch1Axis, ch1Val uint8,
) []byte {
	ovIdx := 0
	for i := range d.Overlays {
		if d.Overlays[i].ID == ovID {
			ovIdx = i
			break
		}
	}
	agIdx := 0
	for i := range d.Overlays[ovIdx].WorkerAgents {
		if d.Overlays[ovIdx].WorkerAgents[i].ID == agentID {
			agIdx = i
			break
		}
	}
	pIdx := 0
	for i := range d.Postures {
		if d.Postures[i].ID == posture {
			pIdx = i
			break
		}
	}
	mIdx := max(0, slices.Index(d.SessionModes, parentMode))
	bIdx := max(0, slices.Index(d.WorkerBranches, branch))
	lIdx := max(0, slices.Index(d.WorkerLoadings, loading))

	return []byte{
		fuzzDecoderVersion1,
		1, // worker
		uint8(ovIdx),
		uint8(pIdx),
		uint8(mIdx),
		uint8(agIdx),
		uint8(bIdx),
		uint8(lIdx),
		0, // RootCount=1
		0, // Approval=Balanced
		0, // ParentUpgraded=false
		0,
		1, // WebSearch=true
		family,
		itemSel,
		numChanges,
		ch1Axis, ch1Val,
		0, 0,
	}
}

// FuzzRecipientGuidanceAcrossHostStates implements CT05 §2d nightly and smoke
// recipient-guidance fuzzing over deterministic byte-decoded two-turn states.
func FuzzRecipientGuidanceAcrossHostStates(f *testing.F) {
	env, err := loadFuzzEnvOnce()
	if err != nil {
		f.Fatalf("loadFuzzEnvOnce: %v", err)
	}
	d := env.domains

	// Seed 1: Reachable coordinator standing + live-job warm-cache control (0 changes).
	f.Add(encodeCoordinatorFuzzBytes(
		d, "stock", api.SessionPostureBuild, api.SessionModeAgent, "implement_investigate",
		hoststate.RootsSingleGit, hoststate.LoadingAllLoaded, hoststate.WorkflowActivePhase,
		gate.PostureBalanced, hoststate.TempScratchAndShared, hoststate.MCPNone,
		0, 0, 0, 0, 0, 0, 0,
	))
	// Seed 2: Reachable worker persona + live-job check.
	f.Add(encodeWorkerFuzzBytes(
		d, "stock", "implementer", api.SessionPostureBuild, api.SessionModeAgent,
		hoststate.BranchIsolatedWorker, hoststate.WorkerLoadingAllLoaded,
		0, 0, 0, 0, 0,
	))
	// Seed 3: Coordinator two-turn transition changing projected capability (Ask -> Agent, axisSel=0).
	f.Add(encodeCoordinatorFuzzBytes(
		d, "stock", api.SessionPostureBuild, api.SessionModeAsk, "implement_investigate",
		hoststate.RootsSingleGit, hoststate.LoadingFloorOnly, hoststate.WorkflowActivePhase,
		gate.PostureBalanced, hoststate.TempScratchOnly, hoststate.MCPNone,
		0, 0, 1, 0, 0, 0, 0,
	))
	// Seed 4: Skill body render across loading transition (LoadingFloorOnly -> LoadingAllLoaded, axisSel=4, valSel=0).
	f.Add(encodeCoordinatorFuzzBytes(
		d, "stock", api.SessionPostureBuild, api.SessionModeAgent, "implement_investigate",
		hoststate.RootsSingleGit, hoststate.LoadingFloorOnly, hoststate.WorkflowActivePhase,
		gate.PostureBalanced, hoststate.TempNone, hoststate.MCPNone,
		1, 0, 1, 4, 0, 0, 0,
	))
	// Seed 5: OAR tool.rejected recovery across posture transition (build -> spec).
	f.Add(encodeCoordinatorFuzzBytes(
		d, "stock", api.SessionPostureBuild, api.SessionModeAgent, "implement_investigate",
		hoststate.RootsSingleGit, hoststate.LoadingAllLoaded, hoststate.WorkflowActivePhase,
		gate.PostureBalanced, hoststate.TempNone, hoststate.MCPNone,
		2, 0, 1, 1, 0, 0, 0,
	))

	sharedWS := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		rep, runErr := executeFuzzRecipientCase(t.Context(), env, data, sharedWS, nil)
		if runErr != nil {
			t.Fatalf("fuzz recipient guidance failed (property=%s, recipient=%s, A=%s, B=%s, contrib=%s, corpus=%s): %v",
				rep.FailedProperty, rep.Recipient, rep.TurnACoords, rep.TurnBCoords, rep.SelectedContribution, rep.CorpusHex, runErr)
		}
	})
}

// TestRecipientGuidanceFuzzSeedsOffMatrixAndMaskedFault verifies CT05 §2d seed
// execution, decoder byte padding/truncation determinism, the off-matrix feasible
// semantic witness, and §2c masked-fault sensitivity + corrected control.
func TestRecipientGuidanceFuzzSeedsOffMatrixAndMaskedFault(t *testing.T) {
	env, err := loadFuzzEnvOnce()
	if err != nil {
		t.Fatalf("loadFuzzEnvOnce: %v", err)
	}
	d := env.domains
	wsDir := t.TempDir()

	// 1. Verify required coordinator, worker, and capability-transition seeds reach intended paths and execute properties.
	coordSeed := encodeCoordinatorFuzzBytes(
		d, "stock", api.SessionPostureBuild, api.SessionModeAgent, "implement_investigate",
		hoststate.RootsSingleGit, hoststate.LoadingAllLoaded, hoststate.WorkflowActivePhase,
		gate.PostureBalanced, hoststate.TempScratchAndShared, hoststate.MCPNone,
		0, 0, 0, 0, 0, 0, 0,
	)
	repCoord, err := executeFuzzRecipientCase(t.Context(), env, coordSeed, wsDir, nil)
	if err != nil || !repCoord.PropertyExecuted || repCoord.Excluded || repCoord.Recipient != "coordinator" {
		t.Fatalf("coordinator seed failed to execute property: rep=%+v err=%v", repCoord, err)
	}

	// Trailing bytes past fuzzDecoderByteLimit must produce the exact same decoded case!
	withTrailing := append(slices.Clone(coordSeed), 0xDE, 0xAD, 0xBE, 0xEF)
	repTrailing, err := executeFuzzRecipientCase(t.Context(), env, withTrailing, wsDir, nil)
	if err != nil || repTrailing.TurnACoords != repCoord.TurnACoords || repTrailing.TurnBCoords != repCoord.TurnBCoords {
		t.Fatalf("decoder did not ignore trailing bytes past limit: %+v vs %+v (err=%v)", repTrailing, repCoord, err)
	}

	// Short/empty byte slice uses zero for missing bytes without panicking.
	if _, err := executeFuzzRecipientCase(t.Context(), env, []byte{}, wsDir, nil); err != nil {
		t.Fatalf("empty byte seed failed: %v", err)
	}

	workerSeed := encodeWorkerFuzzBytes(
		d, "stock", "implementer", api.SessionPostureBuild, api.SessionModeAgent,
		hoststate.BranchIsolatedWorker, hoststate.WorkerLoadingAllLoaded,
		0, 0, 0, 0, 0,
	)
	repWorker, err := executeFuzzRecipientCase(t.Context(), env, workerSeed, wsDir, nil)
	if err != nil || !repWorker.PropertyExecuted || repWorker.Excluded || repWorker.Recipient != "worker" {
		t.Fatalf("worker seed failed to execute property: rep=%+v err=%v", repWorker, err)
	}

	transSeed := encodeCoordinatorFuzzBytes(
		d, "stock", api.SessionPostureBuild, api.SessionModeAsk, "implement_investigate",
		hoststate.RootsSingleGit, hoststate.LoadingFloorOnly, hoststate.WorkflowActivePhase,
		gate.PostureBalanced, hoststate.TempScratchOnly, hoststate.MCPNone,
		0, 0, 1, 0, 0, 0, 0,
	)
	repTrans, err := executeFuzzRecipientCase(t.Context(), env, transSeed, wsDir, nil)
	if err != nil || !repTrans.PropertyExecuted || repTrans.TurnACoords == repTrans.TurnBCoords {
		t.Fatalf("transition seed failed to change capability state and execute property: rep=%+v err=%v", repTrans, err)
	}

	// 2. Feasible off-matrix witness preserved by production identities AND byte seed!
	matrixStates, err := hoststate.CoordinatorStates(t.Context())
	if err != nil {
		t.Fatalf("CoordinatorStates: %v", err)
	}
	matrixIDs := map[string]bool{}
	for _, ms := range matrixStates {
		matrixIDs[ms.ID] = true
	}

	nonStockIdx := 0
	for i, ov := range d.Overlays {
		if ov.ID != "stock" && ov.ID != "without-implement" {
			nonStockIdx = i
			break
		}
	}
	offMatrixProdSeed := hoststate.CoordinatorSeed{
		OverlayIndex:          nonStockIdx,
		SessionMode:           api.SessionModeAsk,
		Posture:               api.SessionPostureSpec,
		WorkflowState:         hoststate.WorkflowWorkersInFlight,
		SurfaceID:             "implement_investigate",
		RootTopology:          hoststate.RootsDualNonGitFirst,
		Loading:               hoststate.LoadingPartlyLoaded,
		WebSearch:             true,
		Vision:                true,
		CommandExternalHidden: true,
		ApprovalPosture:       gate.PostureStrict,
		TempState:             hoststate.TempScratchAndShared,
		MCPState:              hoststate.MCPReadOnlyHint,
	}
	offMatrixState, err := hoststate.ResolveCoordinatorSeed(d, offMatrixProdSeed)
	if err != nil || offMatrixState.Excluded {
		t.Fatalf("off-matrix semantic witness unexpectedly failed or excluded: st=%+v err=%v", offMatrixState, err)
	}
	wasOffMatrix := !matrixIDs[offMatrixState.ID]
	if !wasOffMatrix {
		t.Logf("note: off-matrix witness %s is now included in CoordinatorStates", offMatrixState.ID)
	}
	repOffSemantic, err := runCoordinatorTwoTurnProperties(
		t.Context(), env, env.overlays[nonStockIdx], offMatrixState, offMatrixState, 0, 0, wsDir,
		FuzzCaseReport{Recipient: "coordinator", TurnACoords: offMatrixState.ID, TurnBCoords: offMatrixState.ID, CatalogRevision: env.overlays[nonStockIdx].overlay.Catalog.Revision},
	)
	if err != nil || !repOffSemantic.PropertyExecuted {
		t.Fatalf("off-matrix semantic regression failed: rep=%+v err=%v", repOffSemantic, err)
	}

	// 3. §2c Masked two-axis fault replay: baseline & both 1-axis neighbors pass;
	// combined (Spec/Vet read-only posture + non-Git root) fails on fault-injected
	// bundle and passes on corrected stock.
	baseBytes := encodeCoordinatorFuzzBytes(
		d, "stock", api.SessionPostureBuild, api.SessionModeAgent, "implement_investigate",
		hoststate.RootsSingleGit, hoststate.LoadingAllLoaded, hoststate.WorkflowAmbientIdle,
		gate.PostureBalanced, hoststate.TempNone, hoststate.MCPNone, 0, 0, 0, 0, 0, 0, 0,
	)
	specGitBytes := encodeCoordinatorFuzzBytes(
		d, "stock", api.SessionPostureSpec, api.SessionModeAgent, "implement_investigate",
		hoststate.RootsSingleGit, hoststate.LoadingAllLoaded, hoststate.WorkflowAmbientIdle,
		gate.PostureBalanced, hoststate.TempNone, hoststate.MCPNone, 0, 0, 0, 0, 0, 0, 0,
	)
	agentNonGitBytes := encodeCoordinatorFuzzBytes(
		d, "stock", api.SessionPostureBuild, api.SessionModeAgent, "implement_investigate",
		hoststate.RootsSingleNonGit, hoststate.LoadingAllLoaded, hoststate.WorkflowAmbientIdle,
		gate.PostureBalanced, hoststate.TempNone, hoststate.MCPNone, 0, 0, 0, 0, 0, 0, 0,
	)
	// Combined input transitions Turn A (Build, SingleGit) -> Turn B (Spec, SingleNonGit) via 2 independent axis changes!
	specIdx := uint8(0)
	for i := range d.Postures {
		if d.Postures[i].ID == api.SessionPostureSpec {
			specIdx = uint8(i)
			break
		}
	}
	singleNonGitIdx := uint8(slices.Index(d.RootTopologies, hoststate.RootsSingleNonGit))
	combinedMaskedBytes := encodeCoordinatorFuzzBytes(
		d, "stock", api.SessionPostureBuild, api.SessionModeAgent, "implement_investigate",
		hoststate.RootsSingleGit, hoststate.LoadingAllLoaded, hoststate.WorkflowAmbientIdle,
		gate.PostureBalanced, hoststate.TempNone, hoststate.MCPNone,
		0, 0, 2,
		1, specIdx, // axis 1 sets Posture -> Spec (profile_command_mutation = false)
		3, singleNonGitIdx, // axis 3 sets RootTopology -> RootsSingleNonGit (has_git = false)
	)

	origJobs, _, _ := env.overlays[0].stock.Catalog.UnitContent("guidance/command-jobs")
	maskedJobs := strings.Replace(
		string(origJobs),
		"{% if profile_has_command_output %}",
		"{% if profile_has_command_output and not (not profile_command_mutation and not has_git) %}",
		1,
	)
	maskedEff := buildDisposablePackCatalog(t, "acme/fuzz-masked-fault", map[string]string{
		"guidance/command-jobs.md": maskedJobs,
	}, map[string]string{"guidance/command-jobs": "acme/fuzz-masked-fault"})
	maskedStock := cloneStockWithCatalog(t, env.overlays[0].stock, maskedEff)
	maskedBundle := rebuildFuzzBundleForCatalog(env.overlays[0], maskedStock)

	for lbl, b := range map[string][]byte{
		"baseline_build_git":    baseBytes,
		"neighbor_spec_git":     specGitBytes,
		"neighbor_build_nongit": agentNonGitBytes,
	} {
		if _, nErr := executeFuzzRecipientCase(t.Context(), env, b, wsDir, &maskedBundle); nErr != nil {
			t.Fatalf("expected %s to pass under masked fault, got: %v", lbl, nErr)
		}
	}

	faultRep, faultErr := executeFuzzRecipientCase(t.Context(), env, combinedMaskedBytes, wsDir, &maskedBundle)
	if faultErr == nil || faultRep.FailedProperty != "turn_b_job_occurrence_obligation" {
		t.Fatalf("expected combined masked input to fail turn_b_job_occurrence_obligation, got rep=%+v err=%v", faultRep, faultErr)
	}

	correctedRep, corrErr := executeFuzzRecipientCase(t.Context(), env, combinedMaskedBytes, wsDir, nil)
	if corrErr != nil || !correctedRep.PropertyExecuted {
		t.Fatalf("expected combined masked input to pass on corrected stock, got rep=%+v err=%v", correctedRep, corrErr)
	}

	corpusRel := filepath.Join("testdata", "fuzz", "FuzzRecipientGuidanceAcrossHostStates", "masked_two_axis_spec_nongit")
	wantCorpus := formatGoFuzzV1Bytes(combinedMaskedBytes)
	if os.Getenv("PW_WRITE_FUZZ_CORPUS") == "1" {
		if err := os.MkdirAll(filepath.Dir(corpusRel), 0o755); err != nil {
			t.Fatalf("mkdir corpus dir: %v", err)
		}
		if err := os.WriteFile(corpusRel, []byte(wantCorpus), 0o644); err != nil {
			t.Fatalf("write corpus file: %v", err)
		}
	}
	diskCorpus, err := os.ReadFile(corpusRel)
	if err != nil {
		t.Fatalf("missing committed fuzz corpus file %s: %v", corpusRel, err)
	}
	if string(diskCorpus) != wantCorpus {
		t.Fatalf("committed fuzz corpus %s drifted:\ngot:  %q\nwant: %q", corpusRel, string(diskCorpus), wantCorpus)
	}

	if outDir := strings.TrimSpace(os.Getenv("PW_INTERACTION_LEDGER_DIR")); outDir != "" {
		_ = os.MkdirAll(outDir, 0o755)
		summary := map[string]any{
			"replay_selector":             "go test ./test/contract/invariants/promptrender -run=^FuzzRecipientGuidanceAcrossHostStates$/masked_two_axis_spec_nongit",
			"corpus_file":                 "testdata/fuzz/FuzzRecipientGuidanceAcrossHostStates/masked_two_axis_spec_nongit",
			"corpus_go_syntax":            formatGoFuzzV1Bytes(combinedMaskedBytes),
			"off_matrix_witness_id":       offMatrixState.ID,
			"off_matrix_absent_from_enum": wasOffMatrix,
			"fault_injected_report":       faultRep,
			"fault_injected_error":        faultErr.Error(),
			"corrected_control_report":    correctedRep,
			"total_confirmed_executions":  env.confirmedRuns.Load(),
		}
		raw, _ := json.MarshalIndent(summary, "", "  ")
		_ = os.WriteFile(filepath.Join(outDir, "fuzz-replay-report.json"), raw, 0o644)
	}
}

func rebuildFuzzBundleForCatalog(base fuzzOverlayBundle, st *promptrender.Stock) fuzzOverlayBundle {
	out := base
	out.stock = st
	out.overlay.Catalog = st.Catalog
	out.engine = st.Engine()
	out.injRender = prompts.NewInjectRenderer(out.engine)
	return out
}

func formatGoFuzzV1Bytes(b []byte) string {
	var sb strings.Builder
	sb.WriteString("go test fuzz v1\n[]byte(\"")
	for _, c := range b {
		fmt.Fprintf(&sb, "\\x%02x", c)
	}
	sb.WriteString("\")\n")
	return sb.String()
}
