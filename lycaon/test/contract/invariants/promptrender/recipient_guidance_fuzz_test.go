package promptrender_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcore"
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

// FuzzRecipientGuidanceAcrossHostStates fuzzes recipient guidance over
// deterministic byte-decoded two-turn coordinator and worker states.
func FuzzRecipientGuidanceAcrossHostStates(f *testing.F) {
	env, err := loadFuzzEnvOnce()
	if err != nil {
		f.Fatalf("loadFuzzEnvOnce: %v", err)
	}
	d := env.domains

	// Reachable coordinator standing + live-job warm-cache control (0 changes).
	f.Add(encodeCoordinatorFuzzBytes(
		d, "stock", api.SessionPostureBuild, api.SessionModeAgent, "implement_investigate",
		hoststate.RootsSingleGit, hoststate.LoadingAllLoaded, hoststate.WorkflowActivePhase,
		gate.PostureBalanced, hoststate.TempScratchAndShared, hoststate.MCPNone,
		0, 0, 0, 0, 0, 0, 0,
	))
	// Reachable worker persona + live-job check.
	f.Add(encodeWorkerFuzzBytes(
		d, "stock", "implementer", api.SessionPostureBuild, api.SessionModeAgent,
		hoststate.BranchIsolatedWorker, hoststate.WorkerLoadingAllLoaded,
		0, 0, 0, 0, 0,
	))
	// Coordinator two-turn transition changing projected capability (Ask -> Agent, axisSel=0).
	f.Add(encodeCoordinatorFuzzBytes(
		d, "stock", api.SessionPostureBuild, api.SessionModeAsk, "implement_investigate",
		hoststate.RootsSingleGit, hoststate.LoadingFloorOnly, hoststate.WorkflowActivePhase,
		gate.PostureBalanced, hoststate.TempScratchOnly, hoststate.MCPNone,
		0, 0, 1, 0, 0, 0, 0,
	))
	// Skill body render across loading transition (LoadingFloorOnly -> LoadingAllLoaded, axisSel=4, valSel=0).
	f.Add(encodeCoordinatorFuzzBytes(
		d, "stock", api.SessionPostureBuild, api.SessionModeAgent, "implement_investigate",
		hoststate.RootsSingleGit, hoststate.LoadingFloorOnly, hoststate.WorkflowActivePhase,
		gate.PostureBalanced, hoststate.TempNone, hoststate.MCPNone,
		1, 0, 1, 4, 0, 0, 0,
	))
	// OAR tool.rejected recovery across posture transition (build -> spec).
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

// TestRecipientGuidanceFuzzSeedsOffMatrixAndMaskedFault verifies fuzz seed
// execution, decoder byte padding/truncation determinism, the off-matrix feasible
// semantic witness, and masked-fault sensitivity against corrected stock.
func TestRecipientGuidanceFuzzSeedsOffMatrixAndMaskedFault(t *testing.T) {
	env, err := loadFuzzEnvOnce()
	if err != nil {
		t.Fatalf("loadFuzzEnvOnce: %v", err)
	}
	d := env.domains
	wsDir := t.TempDir()

	// Verify coordinator, worker, and capability-transition seeds execute properties.
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
	withTrailing := append(slices.Clone(coordSeed), 0x00, 0xDE, 0xAD, 0xBE, 0xEF)
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

	// Feasible off-matrix witness preserved by production identities and byte seed.
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

	// Masked two-axis fault replay: baseline and both single-axis neighbors pass;
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
	maskedBundle := rebuildFuzzBundleForCatalog(t, env.overlays[0], maskedStock)

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

	oarCtrlSummary := verifyOARTwoTurnDeliveredRefusalControls(t, env, wsDir, true)

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
			"oar_delivered_refusal":       oarCtrlSummary,
			"total_confirmed_executions":  env.confirmedRuns.Load(),
		}
		raw, _ := json.MarshalIndent(summary, "", "  ")
		_ = os.WriteFile(filepath.Join(outDir, "fuzz-replay-report.json"), raw, 0o644)
	}
}

// TestOARTwoTurnFuzzDeliveredRefusalControls exercises the identical-fix
// delivered-refusal controls, corrected stock neighbor, and 4-way contribution
// distinction in isolation.
func TestOARTwoTurnFuzzDeliveredRefusalControls(t *testing.T) {
	env, err := loadFuzzEnvOnce()
	if err != nil {
		t.Fatalf("loadFuzzEnvOnce: %v", err)
	}
	verifyOARTwoTurnDeliveredRefusalControls(t, env, t.TempDir(), false)
}

func verifyOARTwoTurnDeliveredRefusalControls(t *testing.T, env *fuzzWorkerEnv, wsDir string, sweepAll bool) map[string]any {
	t.Helper()
	d := env.domains
	stockBundle := env.overlays[0]

	ruleIndex := func(shortOrQual string) uint16 {
		for i, r := range stockBundle.rejRules {
			if r.ID == shortOrQual || r.Qualified() == shortOrQual {
				if i > 65535 {
					t.Fatalf("rule index %d exceeds uint16 selector", i)
				}
				return uint16(i)
			}
		}
		t.Fatalf("missing OAR tool.rejected rule %q in bundle (%d rules)", shortOrQual, len(stockBundle.rejRules))
		return 0
	}
	withItemSel16 := func(b []byte, sel uint16) []byte {
		out := slices.Clone(b)
		out[14] = uint8(sel & 0xFF)
		if sel > 255 {
			out = append(out[:20], uint8(sel>>8))
		}
		return out
	}

	if sweepAll {
		// 1. Sweep every stock tool.rejected rule across coordinator and worker two-turn fuzz seeds.
		for i, r := range stockBundle.rejRules {
			if i > 65535 {
				t.Fatalf("rule index %d exceeds uint16 selector", i)
			}
			cSeed := withItemSel16(encodeCoordinatorFuzzBytes(
				d, "stock", api.SessionPostureBuild, api.SessionModeAgent, "implement_investigate",
				hoststate.RootsSingleGit, hoststate.LoadingAllLoaded, hoststate.WorkflowActivePhase,
				gate.PostureBalanced, hoststate.TempScratchOnly, hoststate.MCPNone,
				2, 0, 1, 1, 0, 0, 0,
			), uint16(i))
			if cRep, cErr := executeFuzzRecipientCase(t.Context(), env, cSeed, wsDir, nil); cErr != nil || !cRep.PropertyExecuted {
				t.Errorf("stock coordinator OAR fuzz failed on %s: rep=%+v err=%v", r.Qualified(), cRep, cErr)
			}
			wSeed := withItemSel16(encodeWorkerFuzzBytes(
				d, "stock", "implementer", api.SessionPostureBuild, api.SessionModeAgent,
				hoststate.BranchIsolatedWorker, hoststate.WorkerLoadingAllLoaded,
				2, 0, 0, 0, 0,
			), uint16(i))
			if wRep, wErr := executeFuzzRecipientCase(t.Context(), env, wSeed, wsDir, nil); wErr != nil || !wRep.PropertyExecuted {
				t.Errorf("stock worker OAR fuzz failed on %s: rep=%+v err=%v", r.Qualified(), wRep, wErr)
			}
		}
		if t.Failed() {
			t.FailNow()
		}
	}

	// 2. Deterministic control retaining byte-for-byte identical fix copy while changing
	// an actionable next step in delivered output (Next: / instead), plus passing stock neighbor.
	httpIdx := ruleIndex("HTTP_REQUEST_FAILED")
	stickyIdx := uint8(slices.Index(d.WorkerLoadings, hoststate.WorkerLoadingStickyOnly))
	httpTransSeed := withItemSel16(encodeWorkerFuzzBytes(
		d, "stock", "implementer", api.SessionPostureBuild, api.SessionModeAgent,
		hoststate.BranchIsolatedWorker, hoststate.WorkerLoadingAllLoaded,
		2, 0, 1,
		// Turn A offers fetch_url; Turn B sticky-only loading keeps command and withholds fetch_url.
		3, stickyIdx,
	), httpIdx)

	origHTTPBytes, _, ok := stockBundle.stock.Catalog.UnitContent("policy/HTTP_REQUEST_FAILED")
	if !ok {
		t.Fatal("missing stock policy/HTTP_REQUEST_FAILED")
	}
	origHTTP := string(origHTTPBytes)
	if !strings.Contains(origHTTP, "instead: '{{ paintedwolf.next_steps }}'") {
		t.Fatalf("unexpected stock HTTP_REQUEST_FAILED instead line:\n%s", origHTTP)
	}

	// Control A: identical fix copy; delivered Next: step calls unoffered fetch_url on Turn B.
	badActionHTTP := strings.Replace(
		origHTTP,
		"instead: '{{ paintedwolf.next_steps }}'",
		"instead: '{{ paintedwolf.next_steps }} Or call `fetch_url(url=\"https://example.com\")`.'",
		1,
	)
	badActionEff := replaceStockCatalogUnits(t, map[string]string{"policy/HTTP_REQUEST_FAILED": badActionHTTP})
	badActionBundle := rebuildFuzzBundleForCatalog(t, stockBundle, cloneStockWithCatalog(t, stockBundle.stock, badActionEff))
	badRule, _ := badActionBundle.oarRules.Get("HTTP_REQUEST_FAILED")
	origRule, _ := stockBundle.oarRules.Get("HTTP_REQUEST_FAILED")
	if badRule == nil || origRule == nil || badRule.Copy.Fix != origRule.Copy.Fix {
		t.Fatalf("expected identical fix copy between control and stock neighbor: got %q vs %q", badRule.Copy.Fix, origRule.Copy.Fix)
	}

	badActionRep, badActionErr := executeFuzzRecipientCase(t.Context(), env, httpTransSeed, wsDir, &badActionBundle)
	if badActionErr == nil || badActionRep.FailedProperty != "oar_turn_b_claim_authority" {
		t.Fatalf("expected identical-fix bad next-step control to fail oar_turn_b_claim_authority, got rep=%+v err=%v", badActionRep, badActionErr)
	}

	// Control B: identical fix copy; fire_count changes delivered Next: between warm and fresh Turn B.
	warmDriftHTTP := strings.Replace(
		origHTTP,
		"when: paintedwolf.operation == \"http_request\" && paintedwolf.rejection_code == \"HTTP_REQUEST_FAILED\"\n",
		"when: paintedwolf.operation == \"http_request\" && paintedwolf.rejection_code == \"HTTP_REQUEST_FAILED\" && fire_count >= 0\n",
		1,
	)
	warmDriftHTTP = strings.Replace(
		warmDriftHTTP,
		"effect: block\n",
		"effect: block\non_fire:\n  - increment_counter\n",
		1,
	)
	warmDriftNextSteps := `steps:
  - when: fire_count >= 1
    program: ["git", "status"]
    say: "Inspect workspace state first with {{ call }}."
    otherwise: "Inspect the recorded response and server state before deciding whether to retry."
  - program: ["curl", "--help"]
    say: "When retrying, keep the method, headers, body, and destination explicit in {{ call }}."
    otherwise: "Inspect the recorded response and server state before deciding whether to retry."
`
	warmDriftEff := replaceStockCatalogUnits(t, map[string]string{
		"policy/HTTP_REQUEST_FAILED":          warmDriftHTTP,
		"host/next-steps/HTTP_REQUEST_FAILED": warmDriftNextSteps,
	})
	warmDriftBundle := rebuildFuzzBundleForCatalog(t, stockBundle, cloneStockWithCatalog(t, stockBundle.stock, warmDriftEff))
	driftRule, _ := warmDriftBundle.oarRules.Get("HTTP_REQUEST_FAILED")
	if driftRule == nil || driftRule.Copy.Fix != origRule.Copy.Fix {
		t.Fatalf("expected identical fix copy on warm-drift control: got %q vs %q", driftRule.Copy.Fix, origRule.Copy.Fix)
	}
	warmDriftRep, warmDriftErr := executeFuzzRecipientCase(t.Context(), env, httpTransSeed, wsDir, &warmDriftBundle)
	if warmDriftErr == nil || warmDriftRep.FailedProperty != "oar_warm_vs_fresh_parity" {
		t.Fatalf("expected identical-fix warm-vs-fresh control to fail oar_warm_vs_fresh_parity, got rep=%+v err=%v", warmDriftRep, warmDriftErr)
	}

	// Corrected stock neighbor on the exact same seed bytes must pass and record selected-rule contribution identity.
	stockNeighborRep, stockNeighborErr := executeFuzzRecipientCase(t.Context(), env, httpTransSeed, wsDir, nil)
	if stockNeighborErr != nil || !stockNeighborRep.PropertyExecuted ||
		stockNeighborRep.OAROutcome != "matched_selected_rule" ||
		stockNeighborRep.OmittedOutcome ||
		stockNeighborRep.MatchedContribution != "oar:tool.rejected:"+origRule.Qualified() {
		t.Fatalf("expected corrected stock neighbor to pass with matched_selected_rule, got rep=%+v err=%v", stockNeighborRep, stockNeighborErr)
	}

	// 3. Verify 4-way distinction: normally omitted selected rule vs different matched rule vs fallback.
	contentRejIdx := ruleIndex("CONTENT_APPLY_REJECTED")
	omittedSeed := withItemSel16(encodeCoordinatorFuzzBytes(
		d, "stock", api.SessionPostureSpec, api.SessionModeAgent, "implement_investigate",
		hoststate.RootsSingleGit, hoststate.LoadingAllLoaded, hoststate.WorkflowActivePhase,
		gate.PostureBalanced, hoststate.TempNone, hoststate.MCPNone,
		2, 0, 0, 0, 0, 0, 0,
	), contentRejIdx)
	omittedRep, omittedErr := executeFuzzRecipientCase(t.Context(), env, omittedSeed, wsDir, nil)
	if omittedErr != nil || !omittedRep.PropertyExecuted || omittedRep.OAROutcome != "omitted" || !omittedRep.OmittedOutcome || omittedRep.MatchedContribution != "" {
		t.Fatalf("expected unmatched When rule to report omitted, got rep=%+v err=%v", omittedRep, omittedErr)
	}

	interceptPolicy := `spec_version: "1.0"
namespace: acme.security
id: HTTP_REQUEST_FAILED
kind: schema
on: tool.rejected
selector: {}
requires_facts: [paintedwolf.operation, paintedwolf.rejection_code, paintedwolf.retryable]
when: paintedwolf.operation == "http_request" && paintedwolf.rejection_code == "HTTP_REQUEST_FAILED" && paintedwolf.retryable == false
effect: block
x-paintedwolf-category: recoverable
x-paintedwolf-emit: guard:http_request
message: Intercepted HTTP rejection
what: Intercepted HTTP request failure.
cause: Synthetic higher-precedence invariant rule matched before schema rule.
why: Verifies runOARTwoTurnFuzzCheck distinguishes a different matched rule from the selected rule.
fix: Inspect recorded response state before retrying.
instead: Review the failure details before issuing another call
`
	otherRuleEff := buildDisposablePackCatalog(t, "acme/fuzz-oar-other-rule", map[string]string{
		"policy/DISPOSABLE_HTTP_INTERCEPT.yaml": interceptPolicy,
	}, nil)
	if extpacks.HasErrorDiagnostic(otherRuleEff.Diagnostics) {
		t.Fatalf("otherRuleEff diagnostics: %+v", otherRuleEff.Diagnostics)
	}
	otherRuleBundle := rebuildFuzzBundleForCatalog(t, stockBundle, cloneStockWithCatalog(t, stockBundle.stock, otherRuleEff))
	interceptIdx := uint16(0)
	for i, r := range otherRuleBundle.rejRules {
		if r.Qualified() == "acme.security/HTTP_REQUEST_FAILED" {
			interceptIdx = uint16(i)
			break
		}
	}
	otherRuleSeed := withItemSel16(httpTransSeed, interceptIdx)
	otherRuleRep, otherRuleErr := executeFuzzRecipientCase(t.Context(), env, otherRuleSeed, wsDir, &otherRuleBundle)
	if otherRuleErr != nil || !otherRuleRep.PropertyExecuted ||
		otherRuleRep.OAROutcome != "matched_other_rule" ||
		!otherRuleRep.OmittedOutcome ||
		otherRuleRep.SelectedContribution != "oar:tool.rejected:acme.security/HTTP_REQUEST_FAILED" ||
		otherRuleRep.MatchedContribution != "oar:tool.rejected:"+origRule.Qualified() {
		t.Fatalf("expected higher-precedence rule match to distinguish matched_other_rule from selected rule, got rep=%+v err=%v", otherRuleRep, otherRuleErr)
	}

	fallbackBundle := stockBundle
	fallbackBundle.oarRender = nil
	fallbackRep, fallbackErr := executeFuzzRecipientCase(t.Context(), env, httpTransSeed, wsDir, &fallbackBundle)
	if fallbackErr == nil || fallbackRep.FailedProperty != "oar_unexpected_fallback" {
		t.Fatalf("expected nil-renderer fallback refusal to fail oar_unexpected_fallback, got rep=%+v err=%v", fallbackRep, fallbackErr)
	}

	return map[string]any{
		"stock_rejected_rules_swept":     len(stockBundle.rejRules),
		"identical_fix_bad_next_step":    map[string]any{"report": badActionRep, "error": badActionErr.Error()},
		"identical_fix_warm_fresh_drift": map[string]any{"report": warmDriftRep, "error": warmDriftErr.Error()},
		"corrected_stock_neighbor":       stockNeighborRep,
		"normally_omitted_rule":          omittedRep,
		"matched_other_rule":             otherRuleRep,
		"unexpected_fallback_control":    map[string]any{"report": fallbackRep, "error": fallbackErr.Error()},
	}
}

func replaceStockCatalogUnits(t *testing.T, replacements map[string]string) *extpacks.EffectiveCatalog {
	t.Helper()
	stockContent, err := extpacks.DiscoverStockContent()
	if err != nil {
		t.Fatalf("DiscoverStockContent: %v", err)
	}
	cloned := make([]extpacks.PackContent, len(stockContent))
	matched := map[string]bool{}
	for i, pc := range stockContent {
		pc.Units = slices.Clone(pc.Units)
		for j, u := range pc.Units {
			if body, ok := replacements[u.ID]; ok {
				u.Content = []byte(body)
				pc.Units[j] = u
				matched[u.ID] = true
			}
		}
		cloned[i] = pc
	}
	for unitID := range replacements {
		if !matched[unitID] {
			t.Fatalf("unit %q not found in stock content", unitID)
		}
	}
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   cloned,
		Desired: extpacks.DesiredState{Format: 1},
	})
	if extpacks.HasErrorDiagnostic(eff.Diagnostics) {
		t.Fatalf("replaceStockCatalogUnits diagnostics: %+v", eff.Diagnostics)
	}
	return eff
}

func rebuildFuzzBundleForCatalog(t *testing.T, base fuzzOverlayBundle, st *promptrender.Stock) fuzzOverlayBundle {
	t.Helper()
	out := base
	out.stock = st
	out.overlay.Catalog = st.Catalog
	out.engine = st.Engine()
	out.injRender = prompts.NewInjectRenderer(out.engine)

	schemaDir := filepath.Join(filepath.Dir(configlayout.FindModuleRoot()), "schemas")
	loader, err := oar.NewLoader(schemaDir)
	if err != nil {
		t.Fatalf("oar.NewLoader: %v", err)
	}
	loader.SetToolSchemas(st.Schemas)
	loader.SetDetectors(oar.HostDetectors(nil))
	rs, err := loader.LoadEffectivePolicyWithCatalog(st.Catalog)
	if err != nil {
		t.Fatalf("LoadEffectivePolicyWithCatalog: %v", err)
	}
	var rejRules []*oar.Rule
	for _, r := range rs.All() {
		if r != nil && r.Anchor == oar.AnchorToolRejected && len(oarcore.ConjunctValues(r.When, "paintedwolf.rejection_code")) > 0 {
			rejRules = append(rejRules, r)
		}
	}
	slices.SortFunc(rejRules, func(a, b *oar.Rule) int {
		return strings.Compare(a.Qualified(), b.Qualified())
	})
	hintCfg, err := guidance.LoadHintConfigStock()
	if err != nil {
		t.Fatalf("LoadHintConfigStock: %v", err)
	}
	out.oarLoader = loader
	out.oarRules = rs
	out.rejRules = rejRules
	out.oarRender = oar.NewRenderer(guidance.NewStaticRejectFormatter(hintCfg), noopNudgeFormatter{})
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
