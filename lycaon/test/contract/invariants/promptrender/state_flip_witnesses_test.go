package promptrender_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
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

// Each single host-state flip changes the action a recipient is given: the
// fact, offered tool or parsed call follows the flipped state.
func TestGuidanceFollowsSingleHostStateFlips(t *testing.T) {
	t.Parallel()
	s := mustStock(t)
	states, err := hoststate.CoordinatorStates(t.Context())
	if err != nil {
		t.Fatalf("CoordinatorStates: %v", err)
	}
	investigate := func(mode api.SessionMode, posture api.SessionPosture, loading hoststate.LoadingMode, roots hoststate.RootTopology) hoststate.CoordinatorState {
		return pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == mode && c.Posture == posture && c.TurnProfile.SurfaceID == "implement_investigate" &&
				c.Loading == loading && c.RootTopology == roots
		})
	}

	t.Run("git_commit_follows_primary_root_and_posture", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			st         hoststate.CoordinatorState
			commit     bool
			anyGitRead bool
		}{
			{investigate(api.SessionModeAgent, api.SessionPostureBuild, hoststate.LoadingAllLoaded, hoststate.RootsDualGitFirst), true, true},
			{investigate(api.SessionModeAgent, api.SessionPostureBuild, hoststate.LoadingAllLoaded, hoststate.RootsDualNonGitFirst), false, true},
			{investigate(api.SessionModeAgent, api.SessionPostureSpec, hoststate.LoadingAllLoaded, hoststate.RootsDualGitFirst), false, true},
		} {
			facts := freezeCoord(t, tc.st).Reader.Facts()
			if facts["can_git_commit"] != tc.commit || facts["can_any_git_read"] != tc.anyGitRead {
				t.Errorf("%s: can_git_commit=%v can_any_git_read=%v, want %t %t", tc.st.ID, facts["can_git_commit"], facts["can_any_git_read"], tc.commit, tc.anyGitRead)
			}
		}
	})

	t.Run("verification_call_follows_loading_and_posture", func(t *testing.T) {
		t.Parallel()
		type offer struct{ commandVerify, verify, load bool }
		for _, tc := range []struct {
			st   hoststate.CoordinatorState
			want func(offer) bool
		}{
			{investigate(api.SessionModeAgent, api.SessionPostureBuild, hoststate.LoadingAllLoaded, hoststate.RootsSingleGit), func(o offer) bool { return o.commandVerify || o.verify }},
			{investigate(api.SessionModeAgent, api.SessionPostureBuild, hoststate.LoadingFloorOnly, hoststate.RootsSingleGit), func(o offer) bool { return !o.verify && o.load }},
			{investigate(api.SessionModeAgent, api.SessionPostureSpec, hoststate.LoadingAllLoaded, hoststate.RootsSingleGit), func(o offer) bool { return !o.commandVerify && o.verify }},
			{investigate(api.SessionModeAgent, api.SessionPostureSpec, hoststate.LoadingFloorOnly, hoststate.RootsSingleGit), func(o offer) bool { return !o.commandVerify && !o.verify }},
		} {
			calls := toolvocabtest.ParseCallExamples(s.Names, renderStandingForWitness(t, s, tc.st, "./task test:digest"))
			got := offer{
				commandVerify: slices.ContainsFunc(calls, func(c toolvocabtest.CallExample) bool { return c.Tool == "command" && c.Args["verification"] == true }),
				verify:        slices.ContainsFunc(calls, func(c toolvocabtest.CallExample) bool { return c.Tool == "verify" }),
				load:          slices.ContainsFunc(calls, func(c toolvocabtest.CallExample) bool { return c.Tool == "request_tools" }),
			}
			if !tc.want(got) {
				t.Errorf("%s: parsed verification calls %+v", tc.st.ID, got)
			}
		}
	})

	t.Run("pack_board_follows_attached_folders", func(t *testing.T) {
		t.Parallel()
		park := func(roots hoststate.RootTopology) hoststate.CoordinatorState {
			return pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
				return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
					c.WorkflowState == hoststate.WorkflowWorkersInFlight && c.RootTopology == roots
			})
		}
		oneRoot, zeroRoot := park(hoststate.RootsSingleGit), park(hoststate.RootsNone)
		if zeroRoot.TurnProfile.SurfaceID != "implement_park" {
			t.Fatalf("folderless workers-in-flight surface = %q, want implement_park", zeroRoot.TurnProfile.SurfaceID)
		}
		if !slices.Contains(toolvocab.ToolsIn(s.Names, renderStandingForWitness(t, s, oneRoot, "")), "pack_board") {
			t.Errorf("%s: pack_board not named with one folder attached", oneRoot.ID)
		}
		if slices.Contains(toolvocab.ToolsIn(s.Names, renderStandingForWitness(t, s, zeroRoot, "")), "pack_board") {
			t.Errorf("%s: pack_board named with no folder attached", zeroRoot.ID)
		}
	})

	t.Run("continuation_record_names_recall_only_when_offered", func(t *testing.T) {
		t.Parallel()
		gr := prompts.NewGuidanceRenderer(s.Engine())
		recallMeta, ok := s.Schemas.ToolMeta("recall")
		if !ok {
			t.Fatal("no recall schema")
		}
		occ := map[string]any{
			"level": "references", "omitted_through_ord": 8, "through_ord": 10,
			"compacted_sources": []map[string]any{{"tool": "command", "handles": []string{"read#1"}, "message_id": "m-1"}},
		}
		for _, tc := range []struct {
			reach toolreach.Request
			want  bool
		}{
			{toolreach.Offered([]string{"recall"}).WithSchemas(map[string]map[string]any{"recall": recallMeta.ArgsSchema}), true},
			{toolreach.Offered(nil), false},
		} {
			out, err := gr.RenderWithReach(t.Context(), tc.reach, "compaction/continuation-record", occ)
			if err != nil {
				t.Fatalf("RenderWithReach: %v", err)
			}
			if got := slices.Contains(toolvocab.ToolsIn(s.Names, out), "recall"); got != tc.want {
				t.Errorf("recall named=%t, offered=%t", got, tc.want)
			}
		}
	})

	t.Run("temporary_storage_follows_granted_locations", func(t *testing.T) {
		t.Parallel()
		temp := func(ts hoststate.TempState) hoststate.CoordinatorState {
			return pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
				return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
					c.TurnProfile.SurfaceID == "implement_investigate" && c.RootCount > 0 && c.TempState == ts
			})
		}
		for _, ts := range []hoststate.TempState{hoststate.TempNone, hoststate.TempScratchOnly, hoststate.TempScratchAndShared} {
			st := temp(ts)
			out := renderStandingForWitness(t, s, st, "")
			if got, want := strings.Contains(out, "@scratch"), st.SessionScratchDir != ""; got != want {
				t.Errorf("%s: scratch label delivered=%t, scratch granted=%t", st.ID, got, want)
			}
			for _, loc := range st.SharedTempLocations {
				if !strings.Contains(out, loc) {
					t.Errorf("%s: granted shared temp location %s not delivered", st.ID, loc)
				}
			}
		}
	})

	t.Run("ask_user_secret_follows_session_mode", func(t *testing.T) {
		t.Parallel()
		agentWf := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.TurnProfile.SurfaceID == "orchestrate_plan"
		})
		askWf := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAsk && c.Posture == api.SessionPostureBuild &&
				c.WorkflowState == hoststate.WorkflowActivePhase && c.RequestedSurfaceID == "observe_plan"
		})
		for _, tc := range []struct {
			st   hoststate.CoordinatorState
			want bool
		}{{agentWf, true}, {askWf, false}} {
			vars, err := prompts.SkillVars(freezeCoord(t, tc.st).Reader)
			if err != nil {
				t.Fatalf("SkillVars(%s): %v", tc.st.ID, err)
			}
			if vars["can_ask_user_secret"] != tc.want {
				t.Errorf("%s: can_ask_user_secret=%v, want %t", tc.st.ID, vars["can_ask_user_secret"], tc.want)
			}
		}
	})

	t.Run("command_operation_recovery_follows_command_reach", func(t *testing.T) {
		t.Parallel()
		dispatchFloor := pickCoordState(t, states, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.TurnProfile.SurfaceID == "implement_dispatch" && c.Loading == hoststate.LoadingFloorOnly &&
				c.RootTopology == hoststate.RootsSingleGit
		})
		for _, tc := range []struct {
			st      hoststate.CoordinatorState
			command bool
		}{
			{investigate(api.SessionModeAgent, api.SessionPostureBuild, hoststate.LoadingAllLoaded, hoststate.RootsSingleGit), true},
			{investigate(api.SessionModeAgent, api.SessionPostureSpec, hoststate.LoadingAllLoaded, hoststate.RootsSingleGit), false},
			{dispatchFloor, false},
		} {
			calls := toolvocabtest.ParseCallExamples(s.Names, resolveToolCommandOperationForWitness(t, s, tc.st, "edit"))
			command := slices.ContainsFunc(calls, func(c toolvocabtest.CallExample) bool { return c.Tool == "command" })
			load := slices.ContainsFunc(calls, func(c toolvocabtest.CallExample) bool { return c.Tool == "request_tools" })
			if command != tc.command || (tc.command && load) {
				t.Errorf("%s: TOOL_COMMAND_OPERATION command call=%t request_tools=%t, want command=%t", tc.st.ID, command, load, tc.command)
			}
		}
	})
}

func freezeCoord(t *testing.T, st hoststate.CoordinatorState) hoststate.TurnAuthority {
	t.Helper()
	auth, err := hoststate.FreezeTurnAuthority(t.Context(), st)
	if err != nil {
		t.Fatalf("FreezeTurnAuthority(%s): %v", st.ID, err)
	}
	return auth
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
