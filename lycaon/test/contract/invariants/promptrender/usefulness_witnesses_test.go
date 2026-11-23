package promptrender_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

func mustStock(t *testing.T) *promptrender.Stock {
	t.Helper()
	s, err := promptrender.LoadStock(t.Context())
	if err != nil {
		t.Fatalf("LoadStock: %v", err)
	}
	return s
}

func pickCoordState(t *testing.T, states []hoststate.CoordinatorState, pred func(hoststate.CoordinatorState) bool) hoststate.CoordinatorState {
	t.Helper()
	for _, st := range states {
		if !st.Excluded && st.PackOverlayID == "stock" && pred(st) {
			return st
		}
	}
	t.Fatal("no matching CoordinatorState found")
	return hoststate.CoordinatorState{}
}

func TestCartesianCoverageReportAndZeroDeadOrVacuousBranches(t *testing.T) {
	s := mustStock(t)
	sum, err := promptrender.RunCartesianMatrix(t.Context(), s)
	if err != nil {
		t.Fatalf("RunCartesianMatrix: %v", err)
	}
	if len(sum.Violations) > 0 {
		t.Errorf("Cartesian 7-family matrix reported %d claim violations:\n%s", len(sum.Violations), strings.Join(sum.Violations, "\n"))
	}
	if err := sum.Coverage.Validate(); err != nil {
		t.Errorf("CoverageReport validation failed:\n%v", err)
	}
	if len(sum.Coverage.DiscoveredTemplates) == 0 || len(sum.Coverage.DiscoveredRules) == 0 ||
		len(sum.Coverage.TemplateBranches) == 0 || len(sum.Coverage.RuleBranches) == 0 ||
		sum.Coverage.ReachedCells == 0 || sum.Coverage.ExcludedCells == 0 {
		t.Fatalf("incomplete CoverageReport: %+v", sum.Coverage)
	}
	if !sum.FolderlessParkVerified {
		t.Fatal("expected 0-root WorkflowWorkersInFlight -> implement_park cell to be verified")
	}
	t.Logf(
		"CoverageReport: templates=%d template_branches=%d oar_rules=%d oar_branches=%d reached_cells=%d excluded_cells=%d excluded_reasons=%v cache_hits=%d cache_misses=%d",
		len(sum.Coverage.DiscoveredTemplates),
		len(sum.Coverage.TemplateBranches),
		len(sum.Coverage.DiscoveredRules),
		len(sum.Coverage.RuleBranches),
		sum.Coverage.ReachedCells,
		sum.Coverage.ExcludedCells,
		sum.Coverage.ExcludedByReason,
		sum.CacheHits,
		sum.CacheMisses,
	)
}

func TestPairedUsefulnessWitnessesAcrossSingleAxisFlips(t *testing.T) {
	t.Parallel()
	s := mustStock(t)
	states, err := hoststate.CoordinatorStates(t.Context())
	if err != nil {
		t.Fatalf("CoordinatorStates: %v", err)
	}

	t.Run("can_git_commit_and_mixed_root_git_order", func(t *testing.T) {
		t.Parallel()
		gitPrimary := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingAllLoaded &&
				c.RootTopology == hoststate.RootsDualGitFirst
		})
		nonGitPrimary := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingAllLoaded &&
				c.RootTopology == hoststate.RootsDualNonGitFirst
		})
		specGit := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureSpec &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingAllLoaded &&
				c.RootTopology == hoststate.RootsDualGitFirst
		})

		outGitPrimary := renderStandingForWitness(t, s, gitPrimary, "")
		outNonGitPrimary := renderStandingForWitness(t, s, nonGitPrimary, "")
		outSpecGit := renderStandingForWitness(t, s, specGit, "")

		if !strings.Contains(outGitPrimary, "git commit") || !strings.Contains(outGitPrimary, "git status") {
			t.Fatalf("eligible primary-git build state missing git commit/status:\n%s", outGitPrimary)
		}
		if strings.Contains(outNonGitPrimary, "git commit -m") || !strings.Contains(outNonGitPrimary, `cwd="@`) {
			t.Fatalf("non-git-primary mixed-root state must omit primary git commit and direct reader to cwd=@<label>:\n%s", outNonGitPrimary)
		}
		if strings.Contains(outSpecGit, "git commit -m") || !strings.Contains(outSpecGit, "git status") {
			t.Fatalf("spec posture git state must omit git commit while keeping read-only git status:\n%s", outSpecGit)
		}
		if outGitPrimary == outNonGitPrimary || outGitPrimary == outSpecGit || outNonGitPrimary == outSpecGit {
			t.Fatal("adjacent single-axis flips collapsed to identical generic text")
		}
	})

	t.Run("can_command_verify_callable_vs_loadable_vs_spec", func(t *testing.T) {
		t.Parallel()
		buildLoaded := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingAllLoaded &&
				c.RootTopology == hoststate.RootsSingleGit
		})
		buildFloor := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingFloorOnly &&
				c.RootTopology == hoststate.RootsSingleGit
		})
		specLoaded := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureSpec &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingAllLoaded &&
				c.RootTopology == hoststate.RootsSingleGit
		})
		specFloor := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureSpec &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingFloorOnly &&
				c.RootTopology == hoststate.RootsSingleGit
		})

		outLoaded := renderStandingForWitness(t, s, buildLoaded, "./task test:digest")
		outFloor := renderStandingForWitness(t, s, buildFloor, "./task test:digest")
		outSpec := renderStandingForWitness(t, s, specLoaded, "./task test:digest")
		outSpecFloor := renderStandingForWitness(t, s, specFloor, "./task test:digest")

		if !strings.Contains(outLoaded, "verification: true") && !strings.Contains(outLoaded, "verify()") {
			t.Fatalf("eligible build all_loaded render missing verification call:\n%s", outLoaded)
		}
		if strings.Contains(outFloor, "verify()") || !strings.Contains(outFloor, "request_tools") {
			t.Fatalf("floor_only build render must route through request_tools instead of unoffered verify():\n%s", outFloor)
		}
		if strings.Contains(outSpec, "verification: true") || !strings.Contains(outSpec, "verify()") {
			t.Fatalf("spec posture all_loaded render must omit command(verification: true) while keeping verify():\n%s", outSpec)
		}
		if strings.Contains(outSpecFloor, "verification: true") || strings.Contains(outSpecFloor, "verify()") {
			t.Fatalf("spec posture floor_only render must omit both command(verification: true) and unoffered verify():\n%s", outSpecFloor)
		}
	})

	t.Run("profile_has_pack_board_one_root_vs_zero_root_implement_park", func(t *testing.T) {
		t.Parallel()
		oneRootPark := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.WorkflowState == hoststate.WorkflowWorkersInFlight && c.RootTopology == hoststate.RootsSingleGit
		})
		zeroRootPark := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.WorkflowState == hoststate.WorkflowWorkersInFlight && c.RootTopology == hoststate.RootsNone
		})
		if zeroRootPark.TurnProfile.SurfaceID != "implement_park" {
			t.Fatalf("0-root WorkflowWorkersInFlight surface = %q, want implement_park", zeroRootPark.TurnProfile.SurfaceID)
		}

		outOneRoot := renderStandingForWitness(t, s, oneRootPark, "")
		outZeroRoot := renderStandingForWitness(t, s, zeroRootPark, "")
		if !strings.Contains(outOneRoot, "pack_board") {
			t.Fatalf("1-root implement_park must name pack_board:\n%s", outOneRoot)
		}
		if strings.Contains(outZeroRoot, "`pack_board`") || strings.Contains(outZeroRoot, "pack_board(") {
			t.Fatalf("0-root implement_park must strip pack_board:\n%s", outZeroRoot)
		}
	})

	t.Run("wait_process_done_allowed_and_command_output_loadable", func(t *testing.T) {
		t.Parallel()
		investLoaded := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingAllLoaded &&
				c.RootTopology == hoststate.RootsSingleGit
		})
		investFloor := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingFloorOnly &&
				c.RootTopology == hoststate.RootsSingleGit
		})
		parkLoaded := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.TurnProfile.SurfaceID == "implement_park" && c.Loading == hoststate.LoadingFloorOnly &&
				c.RootTopology == hoststate.RootsSingleGit
		})

		authInvest, _ := hoststate.FreezeTurnAuthority(t.Context(), investLoaded)
		authFloor, _ := hoststate.FreezeTurnAuthority(t.Context(), investFloor)
		authPark, _ := hoststate.FreezeTurnAuthority(t.Context(), parkLoaded)

		now := time.Date(2026, 8, 1, 14, 0, 0, 0, time.UTC)
		jobs := []bgprocess.JobSnapshot{{
			Handle: "cmd-1", Mode: bgprocess.JobModeAwaited, OriginTool: "command",
			StartedAt: now.Add(-15 * time.Second), Stages: []hostcmd.StageResult{{Command: "git status"}},
		}}
		inj := prompts.NewInjectRenderer(s.Engine())

		narrowedWait := toolschema.PruneWaitConditionSchema(authPark.Schemas["wait"], []string{"next_worker_done", "all_workers_idle"})
		outInvest := inject.RenderCommandJobsBlock(t.Context(), inj, authInvest.Reader, authInvest.Schemas["wait"], "s1", jobs, nil, now, "coordinator")
		outFloor := inject.RenderCommandJobsBlock(t.Context(), inj, authFloor.Reader, authFloor.Schemas["wait"], "s2", jobs, nil, now, "coordinator")
		outPark := inject.RenderCommandJobsBlock(t.Context(), inj, authPark.Reader, narrowedWait, "s3", jobs, nil, now, "coordinator")

		if !strings.Contains(outInvest, `"process_done"`) || !strings.Contains(outInvest, `command_output(handle=`) {
			t.Fatalf("investigate all_loaded command-jobs missing process_done wait or direct command_output:\n%s", outInvest)
		}
		if strings.Contains(outFloor, `command_output(handle=`) || !strings.Contains(outFloor, `request_tools(need="command_output")`) {
			t.Fatalf("investigate floor_only command-jobs must route through request_tools(need=\"command_output\"):\n%s", outFloor)
		}
		if strings.Contains(outPark, `"process_done"`) {
			t.Fatalf("narrowed wait command-jobs must omit process_done when wait_conditions exclude process_done:\n%s", outPark)
		}
	})

	t.Run("recall_offered_vs_withheld_in_compaction_and_recall_guidance", func(t *testing.T) {
		t.Parallel()
		gr := prompts.NewGuidanceRenderer(s.Engine())
		recallMeta, _ := s.Schemas.ToolMeta("recall")
		withRecall := toolreach.Offered([]string{"recall"}).WithSchemas(map[string]map[string]any{"recall": recallMeta.ArgsSchema})
		withoutRecall := toolreach.Offered(nil)

		occ := map[string]any{
			"level": "references", "omitted_through_ord": 8, "through_ord": 10,
			"compacted_sources": []map[string]any{{"tool": "command", "handles": []string{"read#1"}, "message_id": "m-1"}},
		}
		pos, err := gr.RenderWithReach(t.Context(), withRecall, "compaction/continuation-record", occ)
		if err != nil {
			t.Fatalf("RenderWithReach with recall: %v", err)
		}
		neg, err := gr.RenderWithReach(t.Context(), withoutRecall, "compaction/continuation-record", occ)
		if err != nil {
			t.Fatalf("RenderWithReach without recall: %v", err)
		}
		if !strings.Contains(pos, "recall") {
			t.Fatalf("continuation-record with recall must name recall:\n%s", pos)
		}
		if strings.Contains(neg, "`recall") || !strings.Contains(neg, "Reacquire omitted observations") {
			t.Fatalf("continuation-record without recall must omit recall and show reacquire fallback:\n%s", neg)
		}
	})

	t.Run("session_scratch_dir_and_shared_temp_writable_flips", func(t *testing.T) {
		t.Parallel()
		noneSt := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.RootCount > 0 &&
				c.TempState == hoststate.TempNone
		})
		scratchSt := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.RootCount > 0 &&
				c.Loading == noneSt.Loading && c.TempState == hoststate.TempScratchOnly
		})
		sharedSt := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.RootCount > 0 &&
				c.TempState == hoststate.TempScratchAndShared
		})

		outNone := renderStandingForWitness(t, s, noneSt, "")
		outScratch := renderStandingForWitness(t, s, scratchSt, "")
		outShared := renderStandingForWitness(t, s, sharedSt, "")

		if strings.Contains(outNone, "`@scratch`") {
			t.Fatalf("TempNone must omit @scratch:\n%s", outNone)
		}
		if !strings.Contains(outScratch, "`@scratch`") || strings.Contains(outScratch, "`/tmp/pw-shared-cache`") {
			t.Fatalf("TempScratchOnly must include @scratch and omit /tmp/pw-shared-cache grant:\n%s", outScratch)
		}
		if !strings.Contains(outShared, "`@scratch`") || !strings.Contains(outShared, "`/tmp/pw-shared-cache`") {
			t.Fatalf("TempScratchAndShared must include both @scratch and /tmp/pw-shared-cache grant:\n%s", outShared)
		}
	})

	t.Run("can_ask_user_secret_agent_vs_ask_narrowed", func(t *testing.T) {
		t.Parallel()
		agentWf := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.TurnProfile.SurfaceID == "orchestrate_plan"
		})
		askWf := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAsk && c.Posture == api.SessionPostureBuild &&
				c.WorkflowState == hoststate.WorkflowActivePhase && c.RequestedSurfaceID == "observe_plan"
		})
		authAgent, _ := hoststate.FreezeTurnAuthority(t.Context(), agentWf)
		authAsk, _ := hoststate.FreezeTurnAuthority(t.Context(), askWf)
		varsAgent, _ := prompts.SkillVars(authAgent.Reader)
		varsAsk, _ := prompts.SkillVars(authAsk.Reader)
		if varsAgent["can_ask_user_secret"] != true {
			t.Fatalf("Agent plan_draft expected can_ask_user_secret=true, got %v", varsAgent["can_ask_user_secret"])
		}
		if varsAsk["can_ask_user_secret"] != false {
			t.Fatalf("Ask observe_plan expected can_ask_user_secret=false, got %v", varsAsk["can_ask_user_secret"])
		}
	})

	t.Run("tool_command_operation_callable_vs_loadable_vs_readonly", func(t *testing.T) {
		t.Parallel()
		buildLoaded := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingAllLoaded &&
				c.RootTopology == hoststate.RootsSingleGit
		})
		buildFloor := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.TurnProfile.SurfaceID == "implement_dispatch" && c.Loading == hoststate.LoadingFloorOnly &&
				c.RootTopology == hoststate.RootsSingleGit
		})
		specLoaded := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureSpec &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingAllLoaded &&
				c.RootTopology == hoststate.RootsSingleGit
		})

		buildAuth, err := hoststate.FreezeTurnAuthority(t.Context(), buildLoaded)
		if err != nil {
			t.Fatalf("FreezeTurnAuthority: %v", err)
		}
		loadableReach := toolreach.Freeze(
			[]string{"request_tools"},
			toolsurface.Compile([]string{"request_tools"}, []string{"command"}),
		).WithProgramTable(buildAuth.ProgramTable).WithSchemas(buildAuth.Schemas)

		outEditCallable := resolveToolCommandOperationForWitness(t, s, buildLoaded, "edit")
		outEditLoadable, lErr := promptrender.EvaluateOARRuleWithReachForTest(
			t.Context(), s, loadableReach, api.SessionModeAgent, api.SessionPostureBuild, "TOOL_COMMAND_OPERATION", "edit",
		)
		if lErr != nil {
			t.Fatalf("EvaluateOARRuleWithReachForTest loadable edit: %v", lErr)
		}
		outEditReadOnly := resolveToolCommandOperationForWitness(t, s, specLoaded, "edit")
		outDispatchUnreachable := resolveToolCommandOperationForWitness(t, s, buildFloor, "edit")

		if !strings.Contains(outEditCallable, "command") || strings.Contains(outEditCallable, "request_tools") {
			t.Fatalf("callable build edit recovery should name command directly without request_tools: %q", outEditCallable)
		}
		if !strings.Contains(outEditLoadable, "request_tools") || !strings.Contains(outEditLoadable, "command") {
			t.Fatalf("loadable edit recovery must include request_tools loading step + command: %q", outEditLoadable)
		}
		if strings.Contains(outEditReadOnly, "command(") || !strings.Contains(outEditReadOnly, `cannot run a replacement form for "edit"`) {
			t.Fatalf("spec read-only edit recovery must not offer mutating command call and must explain limitation: %q", outEditReadOnly)
		}
		if strings.Contains(outDispatchUnreachable, "command(") || !strings.Contains(outDispatchUnreachable, `cannot run a replacement form for "edit"`) {
			t.Fatalf("dispatch surface without command must not offer command call: %q", outDispatchUnreachable)
		}
	})
}

func renderStandingForWitness(t *testing.T, s *promptrender.Stock, st hoststate.CoordinatorState, verifyCmd string) string {
	t.Helper()
	text, err := promptrender.RenderCoordinatorStandingForTest(t.Context(), s, st, verifyCmd)
	if err != nil {
		t.Fatalf("RenderCoordinatorStandingForTest(%s): %v", st.ID, err)
	}
	return text
}

func resolveToolCommandOperationForWitness(t *testing.T, s *promptrender.Stock, st hoststate.CoordinatorState, need string) string {
	t.Helper()
	out, err := promptrender.EvaluateOARRuleCopyForTest(t.Context(), s, st, "TOOL_COMMAND_OPERATION", need)
	if err != nil {
		t.Fatalf("EvaluateOARRuleCopyForTest(%s, %q): %v", st.ID, need, err)
	}
	return out
}
