package promptrender_test

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

// encodeCoordinatorFuzzBytes encodes a coordinator case with web search on.
func encodeCoordinatorFuzzBytes(
	tb testing.TB,
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
	tb.Helper()
	b, err := d.EncodeCoordinator(hoststate.CoordinatorSeed{
		OverlayIndex:    overlayIndexByID(tb, d, ovID),
		SessionMode:     mode,
		Posture:         posture,
		WorkflowState:   wf,
		SurfaceID:       surfID,
		RootTopology:    roots,
		Loading:         loading,
		WebSearch:       true,
		ApprovalPosture: app,
		TempState:       temp,
		MCPState:        mcp,
	}, hoststate.SeedTail{
		Family: family, Item: uint16(itemSel), ChangeCount: numChanges,
		Changes: [2]hoststate.SeedChange{{Axis: ch1Axis, Value: ch1Val}, {Axis: ch2Axis, Value: ch2Val}},
	})
	if err != nil {
		tb.Fatalf("encode coordinator seed: %v", err)
	}
	return b
}

// encodeWorkerFuzzBytes encodes a one-root worker case with balanced approval
// and web search on.
func encodeWorkerFuzzBytes(
	tb testing.TB,
	d *hoststate.SeedDomains,
	ovID, agentID string,
	posture api.SessionPosture,
	parentMode api.SessionMode,
	branch hoststate.WorkerBranchView,
	loading hoststate.WorkerLoadingMode,
	family, itemSel, numChanges, ch1Axis, ch1Val uint8,
) []byte {
	tb.Helper()
	b, err := d.EncodeWorker(hoststate.WorkerSeed{
		OverlayIndex:      overlayIndexByID(tb, d, ovID),
		AgentID:           agentID,
		Posture:           posture,
		BranchView:        branch,
		RootCount:         1,
		Loading:           loading,
		ParentSessionMode: parentMode,
		WebSearch:         true,
		ApprovalPosture:   gate.PostureBalanced,
	}, hoststate.SeedTail{
		Family: family, Item: uint16(itemSel), ChangeCount: numChanges,
		Changes: [2]hoststate.SeedChange{{Axis: ch1Axis, Value: ch1Val}},
	})
	if err != nil {
		tb.Fatalf("encode worker seed: %v", err)
	}
	return b
}

func overlayIndexByID(tb testing.TB, d *hoststate.SeedDomains, id string) int {
	tb.Helper()
	i := slices.IndexFunc(d.Overlays, func(o hoststate.PackOverlay) bool { return o.ID == id })
	if i < 0 {
		tb.Fatalf("no overlay %q", id)
	}
	return i
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
		f, d, "stock", api.SessionPostureBuild, api.SessionModeAgent, "implement_investigate",
		hoststate.RootsSingleGit, hoststate.LoadingAllLoaded, hoststate.WorkflowActivePhase,
		gate.PostureBalanced, hoststate.TempScratchAndShared, hoststate.MCPNone,
		0, 0, 0, 0, 0, 0, 0,
	))
	// Reachable worker persona + live-job check.
	f.Add(encodeWorkerFuzzBytes(
		f, d, "stock", "implementer", api.SessionPostureBuild, api.SessionModeAgent,
		hoststate.BranchIsolatedWorker, hoststate.WorkerLoadingAllLoaded,
		0, 0, 0, 0, 0,
	))
	// Coordinator two-turn transition changing projected capability (Ask -> Agent, axisSel=0).
	f.Add(encodeCoordinatorFuzzBytes(
		f, d, "stock", api.SessionPostureBuild, api.SessionModeAsk, "implement_investigate",
		hoststate.RootsSingleGit, hoststate.LoadingFloorOnly, hoststate.WorkflowActivePhase,
		gate.PostureBalanced, hoststate.TempScratchOnly, hoststate.MCPNone,
		0, 0, 1, 0, 0, 0, 0,
	))
	// Skill body render across loading transition (LoadingFloorOnly -> LoadingAllLoaded, axisSel=4, valSel=0).
	f.Add(encodeCoordinatorFuzzBytes(
		f, d, "stock", api.SessionPostureBuild, api.SessionModeAgent, "implement_investigate",
		hoststate.RootsSingleGit, hoststate.LoadingFloorOnly, hoststate.WorkflowActivePhase,
		gate.PostureBalanced, hoststate.TempNone, hoststate.MCPNone,
		1, 0, 1, 4, 0, 0, 0,
	))
	// OAR tool.rejected recovery across posture transition (build -> spec).
	f.Add(encodeCoordinatorFuzzBytes(
		f, d, "stock", api.SessionPostureBuild, api.SessionModeAgent, "implement_investigate",
		hoststate.RootsSingleGit, hoststate.LoadingAllLoaded, hoststate.WorkflowActivePhase,
		gate.PostureBalanced, hoststate.TempNone, hoststate.MCPNone,
		2, 0, 1, 1, 0, 0, 0,
	))

	f.Fuzz(func(t *testing.T, data []byte) {
		rep, runErr := executeFuzzRecipientCase(t.Context(), env, data, t.TempDir())
		if runErr != nil {
			t.Fatalf("fuzz recipient guidance failed (property=%s, recipient=%s, A=%s, B=%s, contrib=%s, corpus=%s): %v",
				rep.FailedProperty, rep.Recipient, rep.TurnACoords, rep.TurnBCoords, rep.SelectedContribution, rep.CorpusHex, runErr)
		}
	})
}

// A feasible coordinator state outside the enumerated matrix, resolved through
// the same production identities, satisfies the recipient properties.
func TestRecipientGuidanceOnOffMatrixCoordinatorState(t *testing.T) {
	env, err := loadFuzzEnvOnce()
	if err != nil {
		t.Fatalf("loadFuzzEnvOnce: %v", err)
	}
	d := env.domains
	nonStockIdx := slices.IndexFunc(d.Overlays, func(ov hoststate.PackOverlay) bool {
		return ov.ID != "stock" && ov.ID != "without-implement"
	})
	if nonStockIdx < 0 {
		t.Fatal("no site overlay in the seed domains")
	}
	st, err := hoststate.ResolveCoordinatorSeed(d, hoststate.CoordinatorSeed{
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
	})
	if err != nil || st.Excluded {
		t.Fatalf("off-matrix state failed or excluded: st=%+v err=%v", st, err)
	}
	for family := range 3 {
		rep, err := runCoordinatorTwoTurnProperties(
			t.Context(), env, env.overlays[nonStockIdx], st, st, family, 0, t.TempDir(),
			FuzzCaseReport{Recipient: "coordinator", TurnACoords: st.ID, TurnBCoords: st.ID},
		)
		if err != nil {
			t.Fatalf("family %d on %s (property=%s): %v", family, st.ID, rep.FailedProperty, err)
		}
	}
}

// Every stock tool.rejected rule delivers a true refusal to a coordinator and a
// worker across a two-turn transition, warm and fresh.
func TestRejectedRulesAcrossTwoTurnRecipients(t *testing.T) {
	env, err := loadFuzzEnvOnce()
	if err != nil {
		t.Fatalf("loadFuzzEnvOnce: %v", err)
	}
	d := env.domains
	stockBundle := env.overlays[0]
	withItemSel16 := func(b []byte, sel int) []byte {
		out := slices.Clone(b)
		out[14] = uint8(sel & 0xFF)
		if sel > 255 {
			out = append(out[:20], uint8(sel>>8))
		}
		return out
	}
	for i, r := range stockBundle.rejRules {
		if i > 65535 {
			t.Fatalf("rule index %d exceeds the selector", i)
		}
		cSeed := withItemSel16(encodeCoordinatorFuzzBytes(
			t, d, "stock", api.SessionPostureBuild, api.SessionModeAgent, "implement_investigate",
			hoststate.RootsSingleGit, hoststate.LoadingAllLoaded, hoststate.WorkflowActivePhase,
			gate.PostureBalanced, hoststate.TempScratchOnly, hoststate.MCPNone,
			2, 0, 1, 1, 0, 0, 0,
		), i)
		if cRep, cErr := executeFuzzRecipientCase(t.Context(), env, cSeed, t.TempDir()); cErr != nil || !cRep.PropertyExecuted {
			t.Errorf("coordinator %s: rep=%+v err=%v", r.Qualified(), cRep, cErr)
		}
		wSeed := withItemSel16(encodeWorkerFuzzBytes(
			t, d, "stock", "implementer", api.SessionPostureBuild, api.SessionModeAgent,
			hoststate.BranchIsolatedWorker, hoststate.WorkerLoadingAllLoaded,
			2, 0, 0, 0, 0,
		), i)
		if wRep, wErr := executeFuzzRecipientCase(t.Context(), env, wSeed, t.TempDir()); wErr != nil || !wRep.PropertyExecuted {
			t.Errorf("worker %s: rep=%+v err=%v", r.Qualified(), wRep, wErr)
		}
	}

	httpIdx := slices.IndexFunc(stockBundle.rejRules, func(r *oar.Rule) bool { return r.ID == "HTTP_REQUEST_FAILED" })
	if httpIdx < 0 {
		t.Fatal("no stock HTTP_REQUEST_FAILED rule")
	}
	stickyIdx := uint8(slices.Index(d.WorkerLoadings, hoststate.WorkerLoadingStickyOnly))
	// Turn A offers fetch_url; sticky-only loading on turn B keeps command and withholds it.
	httpTransSeed := withItemSel16(encodeWorkerFuzzBytes(
		t, d, "stock", "implementer", api.SessionPostureBuild, api.SessionModeAgent,
		hoststate.BranchIsolatedWorker, hoststate.WorkerLoadingAllLoaded,
		2, 0, 1, 3, stickyIdx,
	), httpIdx)
	rep, err := executeFuzzRecipientCase(t.Context(), env, httpTransSeed, t.TempDir())
	if err != nil || rep.OAROutcome != "matched_selected_rule" ||
		rep.MatchedContribution != "oar:tool.rejected:"+stockBundle.rejRules[httpIdx].Qualified() {
		t.Fatalf("HTTP_REQUEST_FAILED on a fetch_url-withholding turn: rep=%+v err=%v", rep, err)
	}
}
