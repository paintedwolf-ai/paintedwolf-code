package promptrender_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

func verifyPairedUsefulnessSection(
	artifact, subject, eligibleState, ineligibleState string,
	eligibleRender, ineligibleRender string,
	requiredAction string,
) error {
	if !strings.Contains(eligibleRender, requiredAction) {
		return fmt.Errorf("%s [%s] on %s deleted required positive action %q", artifact, subject, eligibleState, requiredAction)
	}
	if strings.Contains(ineligibleRender, requiredAction) {
		return fmt.Errorf("%s [%s] on %s leaked forbidden action %q", artifact, subject, ineligibleState, requiredAction)
	}
	if strings.TrimSpace(eligibleRender) == strings.TrimSpace(ineligibleRender) {
		return fmt.Errorf("%s [%s] collapsed %s and %s to identical generic fallback", artifact, subject, eligibleState, ineligibleState)
	}
	return nil
}

func TestEightMandatorySeededFaultsFailPinpointAndHarmlessRewordingsPass(t *testing.T) {
	s := mustStock(t)
	coordStates, err := hoststate.CoordinatorStates(t.Context())
	if err != nil {
		t.Fatalf("CoordinatorStates: %v", err)
	}
	workerStates, err := hoststate.WorkerStates(t.Context())
	if err != nil {
		t.Fatalf("WorkerStates: %v", err)
	}
	skillsList, err := promptrender.StockSkills(s)
	if err != nil {
		t.Fatalf("StockSkills: %v", err)
	}

	// Early deferred load in skill reader: skillToolReader marks a deferred tool loaded
	// when absent from TurnOfferedToolNames (floor_only turn).
	t.Run("seed_1_early_deferred_load_in_skill_reader", func(t *testing.T) {
		t.Parallel()
		floorSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingFloorOnly &&
				c.RootTopology == hoststate.RootsSingleGit
		})
		floorAuth, fErr := hoststate.FreezeTurnAuthority(t.Context(), floorSt)
		if fErr != nil {
			t.Fatalf("FreezeTurnAuthority: %v", fErr)
		}
		// Faulty skill reader: marks deferred "verify" as loaded/visible even though floorAuth.Offered omits "verify".
		faultyReader, rErr := prompts.NewReader(prompts.ReaderInput{
			ProfileID: prompts.CoordinatorProfileID, SurfaceID: "implement_investigate",
			Profiles: s.Profiles, Plan: floorAuth.Plan,
			Loaded:  map[string]bool{"verify": true},
			Visible: append(append([]string(nil), floorAuth.Offered...), "verify"),
			Schemas: s.Schemas, RootCount: 1, HasGit: true, WebSearch: true,
		})
		if rErr != nil {
			t.Fatalf("faulty NewReader: %v", rErr)
		}
		var verifySkillIdx = -1
		for i := range skillsList {
			if skillsList[i].Name == "verify-a-change" {
				verifySkillIdx = i
				break
			}
		}
		if verifySkillIdx < 0 {
			t.Fatal("missing stock skill verify-a-change")
		}
		badVars, _ := prompts.SkillVars(faultyReader)
		badParts, _ := promptrender.RenderSkill(skillsList[verifySkillIdx], badVars)
		label := "skill verify-a-change [self_now] (" + floorSt.ID + ")"
		viols := promptrender.EvaluateClaimsForTest(s, label, badParts[0].Text, floorAuth, floorSt.SessionMode, floorSt.Posture, toolvocabtest.SubjectSelfNow(), true)
		if len(viols) == 0 || !strings.Contains(strings.Join(viols, "\n"), "verify-a-change") || !strings.Contains(strings.Join(viols, "\n"), "verify") {
			t.Fatalf("expected early deferred skill load fault to fail naming skill, subject, and state; got %v", viols)
		}

		// True floor_only skill render passes cleanly.
		goodVars, _ := prompts.SkillVars(floorAuth.Reader)
		goodParts, _ := promptrender.RenderSkill(skillsList[verifySkillIdx], goodVars)
		if goodViols := promptrender.EvaluateClaimsForTest(s, label, goodParts[0].Text, floorAuth, floorSt.SessionMode, floorSt.Posture, toolvocabtest.SubjectSelfNow(), true); len(goodViols) > 0 {
			t.Fatalf("expected honest floor_only skill render to pass, got %v", goodViols)
		}
	})

	// Cross-tool argument misattribution: giving command an argument (prompt) accepted
	// only by ask_user on the same turn.
	t.Run("seed_2_cross_tool_argument_misattribution", func(t *testing.T) {
		t.Parallel()
		planSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingAllLoaded &&
				c.RootCount > 0
		})
		auth, fErr := hoststate.FreezeTurnAuthority(t.Context(), planSt)
		if fErr != nil {
			t.Fatalf("FreezeTurnAuthority: %v", fErr)
		}
		artifact := "standing/implement_investigate [self_now] (" + planSt.ID + ")"
		seeded := `Inspect the workspace via ` + "`command(command=\"ls\", prompt=\"Proceed?\")`" + `.`
		viols := promptrender.EvaluateClaimsForTest(s, artifact, seeded, auth, planSt.SessionMode, planSt.Posture, toolvocabtest.SubjectSelfNow(), true)
		joined := strings.Join(viols, "\n")
		if len(viols) == 0 || !strings.Contains(joined, "implement_investigate") || !strings.Contains(joined, "prompt") {
			t.Fatalf("expected cross-tool argument misattribution to fail naming artifact, subject, and state; got %v", viols)
		}

		reworded := `Inspect the workspace via ` + "`command(command=\"ls\")`" + `, and ask blocking questions via ` + "`ask_user(prompt=\"Proceed?\")`" + `.`
		if okViols := promptrender.EvaluateClaimsForTest(s, artifact, reworded, auth, planSt.SessionMode, planSt.Posture, toolvocabtest.SubjectSelfNow(), true); len(okViols) > 0 {
			t.Fatalf("harmless rewording failed: %v", okViols)
		}
	})

	// Narrowed wait enum mismatch: instructing wait(..., conditions=[{"kind":"process_done",...}])
	// on a profile whose wait_conditions exclude "process_done" (stock web_research profile).
	t.Run("seed_3_narrowed_wait_enum_mismatch", func(t *testing.T) {
		var webWorker hoststate.WorkerState
		found := false
		for _, ws := range workerStates {
			if !ws.Excluded && ws.ToolProfile == "web_research" && ws.Loading == hoststate.WorkerLoadingStickyOnly {
				webWorker = ws
				found = true
				break
			}
		}
		if !found {
			t.Fatal("missing web_research WorkerState")
		}
		auth, fErr := hoststate.FreezeWorkerAuthority(t.Context(), webWorker)
		if fErr != nil {
			t.Fatalf("FreezeWorkerAuthority: %v", fErr)
		}
		artifact := "inject/command-jobs [self_now] (" + webWorker.ID + ")"
		seeded := `Await background completion with ` + "`wait(until_complete=true, conditions=[{\"kind\":\"process_done\",\"handles\":[\"cmd-1\"]}])`" + `.`
		viols := promptrender.EvaluateClaimsForTest(s, artifact, seeded, auth, webWorker.ParentSessionMode, webWorker.Posture, toolvocabtest.SubjectSelfNow(), true)
		joined := strings.Join(viols, "\n")
		if len(viols) == 0 || !strings.Contains(joined, "command-jobs") || !strings.Contains(joined, "process_done") {
			t.Fatalf("expected narrowed wait enum mismatch to fail naming command-jobs and process_done; got %v", viols)
		}

		reworded := `Await service readiness with ` + "`wait(timeout_ms=5000, conditions=[{\"kind\":\"http_ready\",\"url\":\"http://127.0.0.1:8080/health\"}])`" + `.`
		if okViols := promptrender.EvaluateClaimsForTest(s, artifact, reworded, auth, webWorker.ParentSessionMode, webWorker.Posture, toolvocabtest.SubjectSelfNow(), true); len(okViols) > 0 {
			t.Fatalf("harmless wait enum rewording failed: %v", okViols)
		}
	})

	// Secondary-root Git misattribution: claiming primary-root git commit / git status
	// when (primary=non-git, secondary=git).
	t.Run("seed_4_secondary_root_git_misattribution", func(t *testing.T) {
		t.Parallel()
		mixedNonGitFirst := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingAllLoaded &&
				c.RootTopology == hoststate.RootsDualNonGitFirst
		})
		auth, fErr := hoststate.FreezeTurnAuthority(t.Context(), mixedNonGitFirst)
		if fErr != nil {
			t.Fatalf("FreezeTurnAuthority: %v", fErr)
		}
		if auth.Reader.Facts()["can_git_read"] != false || auth.Reader.Facts()["can_any_git_read"] != true {
			t.Fatalf("unexpected mixed-root git facts: can_git_read=%v can_any_git_read=%v",
				auth.Reader.Facts()["can_git_read"], auth.Reader.Facts()["can_any_git_read"])
		}
		artifact := "unit/git-mutation-user-consent [self_now] (" + mixedNonGitFirst.ID + ")"
		// Seeded copy falsely claims primary-root git status / git commit without cwd=@<label> when HasGit==false.
		seededPrimaryGitClaim := "Run bare `git status` on the primary folder and commit via `git commit -m \"msg\" -- main.go`."
		checkMixedGit := func(text string) error {
			if !mixedNonGitFirst.HasGit && mixedNonGitFirst.HasAnyGitRoot {
				if strings.Contains(text, "`git status`") && !strings.Contains(text, `cwd="@`) {
					return fmt.Errorf("%s claims primary-root `git status` without cwd=@<label> on state %s (primary=non-git, secondary=git)", artifact, mixedNonGitFirst.ID)
				}
			}
			return nil
		}
		if err := checkMixedGit(seededPrimaryGitClaim); err == nil || !strings.Contains(err.Error(), mixedNonGitFirst.ID) {
			t.Fatalf("expected secondary-root Git misattribution to fail naming artifact and state, got %v", err)
		}
		reworded := "Pass `cwd=\"@secondary\"` in `command` to run `git status` in the attached Git folder."
		if err := checkMixedGit(reworded); err != nil {
			t.Fatalf("harmless secondary-root Git guidance failed: %v", err)
		}
		if okViols := promptrender.EvaluateClaimsForTest(s, artifact, reworded, auth, mixedNonGitFirst.SessionMode, mixedNonGitFirst.Posture, toolvocabtest.SubjectSelfNow(), true); len(okViols) > 0 {
			t.Fatalf("harmless secondary-root Git guidance failed claim check: %v", okViols)
		}
	})

	// Omitted mode transition / stale child upgrade: granting mutation to an Ask-spawned
	// read-only child worker after root upgrades Ask -> Agent, or omitting session.mode.agent_enabled.
	t.Run("seed_5_omitted_mode_transition_or_stale_child_upgrade", func(t *testing.T) {
		t.Parallel()
		var askChild hoststate.WorkerState
		found := false
		for _, ws := range workerStates {
			if !ws.Excluded && ws.ParentSessionMode == api.SessionModeAsk && ws.Posture == api.SessionPostureBuild {
				askChild = ws
				found = true
				break
			}
		}
		if !found {
			t.Fatal("missing Ask-spawned WorkerState")
		}
		childAuth, fErr := hoststate.FreezeWorkerAuthority(t.Context(), askChild)
		if fErr != nil {
			t.Fatalf("FreezeWorkerAuthority: %v", fErr)
		}
		artifact := "worker/" + askChild.AgentID + " [self_now] (" + askChild.ID + ")"
		seededMutating := "Root upgraded to Agent; apply edits with `edit --path a.go --old-string x --new-string y` in `command`."
		viols := promptrender.EvaluateClaimsForTest(s, artifact, seededMutating, childAuth, api.SessionModeAsk, askChild.Posture, toolvocabtest.SubjectSelfNow(), true)
		if len(viols) == 0 || !strings.Contains(strings.Join(viols, "\n"), askChild.AgentID) {
			t.Fatalf("expected stale Ask-child mutation claim to fail naming worker artifact and state; got %v", viols)
		}

		rewordedReadOnly := "Remain within read-only inspection and finish with `complete_leg(leg_status=\"complete\", brief=\"…\")`."
		if okViols := promptrender.EvaluateClaimsForTest(s, artifact, rewordedReadOnly, childAuth, api.SessionModeAsk, askChild.Posture, toolvocabtest.SubjectSelfNow(), true); len(okViols) > 0 {
			t.Fatalf("harmless read-only Ask-child guidance failed: %v", okViols)
		}
	})

	// Stale mutating recovery on read-only surface: recommending command for need="edit"
	// in TOOL_COMMAND_OPERATION when CommandMutation == false (spec posture).
	t.Run("seed_6_stale_mutating_recovery_on_readonly_surface", func(t *testing.T) {
		t.Parallel()
		specSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureSpec &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingAllLoaded &&
				c.RootCount > 0
		})
		specAuth, fErr := hoststate.FreezeTurnAuthority(t.Context(), specSt)
		if fErr != nil {
			t.Fatalf("FreezeTurnAuthority: %v", fErr)
		}
		artifact := "oar-next-steps/TOOL_COMMAND_OPERATION [self_now] (" + specSt.ID + ")"
		seeded := "Run `edit --path internal/a.go --old-string old --new-string new` in `command`."
		viols := promptrender.EvaluateClaimsForTest(s, artifact, seeded, specAuth, specSt.SessionMode, specSt.Posture, toolvocabtest.SubjectSelfNow(), false)
		joined := strings.Join(viols, "\n")
		if len(viols) == 0 || !strings.Contains(joined, "TOOL_COMMAND_OPERATION") || !strings.Contains(joined, "edit") {
			t.Fatalf("expected stale mutating recovery on read-only spec surface to fail; got %v", viols)
		}

		reworded := "This surface does not admit file mutations; inspect lines with `cat -n internal/a.go` in `command`."
		if okViols := promptrender.EvaluateClaimsForTest(s, artifact, reworded, specAuth, specSt.SessionMode, specSt.Posture, toolvocabtest.SubjectSelfNow(), false); len(okViols) > 0 {
			t.Fatalf("harmless read-only spec recovery failed: %v", okViols)
		}
	})

	// Dead conditional branch: adding an unreachable {% if %} branch (such as read_offered)
	// to a template fails CoverageTracker.Report().Validate().
	t.Run("seed_7_dead_or_vacuous_conditional_branch", func(t *testing.T) {
		// Do not run in parallel with other global ActiveBranchRecorder swaps in this process.
		tracker := hoststate.NewCoverageTracker()
		const badTpl = "Inspect state.{% if read_offered %} Call read directly.{% endif %}"
		if rErr := tracker.RegisterTemplate("seeded/dead-branch.md", badTpl, pongoplain.Isolated); rErr != nil {
			t.Fatalf("RegisterTemplate: %v", rErr)
		}
		restore := tracker.Install()
		tpl, cErr := pongoplain.Compile(badTpl)
		if cErr != nil {
			restore()
			t.Fatalf("Compile: %v", cErr)
		}
		_, _ = pongoplain.ExecuteDetached(tpl, map[string]any{"profile_has_command": true})
		_, _ = pongoplain.ExecuteDetached(tpl, map[string]any{"profile_has_command": false})
		restore()

		vErr := tracker.Report().Validate()
		if vErr == nil || !strings.Contains(vErr.Error(), "seeded/dead-branch.md") {
			t.Fatalf("expected unreachable {%% if read_offered %%} branch to fail CoverageReport.Validate(), got %v", vErr)
		}

		// Equivalent realizable branch hits both true and false cleanly.
		okTracker := hoststate.NewCoverageTracker()
		const okTpl = "Inspect state.{% if profile_has_command %} Use `command`.{% endif %}"
		if rErr := okTracker.RegisterTemplate("seeded/live-branch.md", okTpl, pongoplain.Isolated); rErr != nil {
			t.Fatalf("RegisterTemplate ok: %v", rErr)
		}
		restoreOK := okTracker.Install()
		compiledOK, _ := pongoplain.Compile(okTpl)
		_, _ = pongoplain.ExecuteDetached(compiledOK, map[string]any{"profile_has_command": true})
		_, _ = pongoplain.ExecuteDetached(compiledOK, map[string]any{"profile_has_command": false})
		restoreOK()
		if okErr := okTracker.Report().Validate(); okErr != nil {
			t.Fatalf("expected realizable conditional branch to pass CoverageReport.Validate(), got %v", okErr)
		}
	})

	// Deleted positive guidance: deleting a required capability-specific instruction block
	// while leaving negative scans clean is caught by the paired usefulness contract.
	t.Run("seed_8_deleted_positive_guidance", func(t *testing.T) {
		t.Parallel()
		buildSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingAllLoaded &&
				c.RootTopology == hoststate.RootsSingleGit
		})
		specSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
			return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureSpec &&
				c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingAllLoaded &&
				c.RootTopology == hoststate.RootsSingleGit
		})
		buildAuth, _ := hoststate.FreezeTurnAuthority(t.Context(), buildSt)

		// Seeded render deletes git commit instructions even on eligible buildSt; negative reach scan stays 0 violations!
		deletedPositive := "Git is read-only on this surface. Read status with `git status`."
		specRender := "Git is read-only on this surface. Read status with `git status`."
		if negViols := promptrender.EvaluateClaimsForTest(s, "unit/git-mutation", deletedPositive, buildAuth, buildSt.SessionMode, buildSt.Posture, toolvocabtest.SubjectSelfNow(), true); len(negViols) != 0 {
			t.Fatalf("seeded deleted-positive text unexpectedly triggered negative scan: %v", negViols)
		}
		pErr := verifyPairedUsefulnessSection(
			"shared/units/git-mutation-user-consent.md", "self_now",
			buildSt.ID, specSt.ID,
			deletedPositive, specRender,
			"git commit",
		)
		if pErr == nil || !strings.Contains(pErr.Error(), "git-mutation-user-consent.md") || !strings.Contains(pErr.Error(), buildSt.ID) {
			t.Fatalf("expected deleted positive guidance to fail naming artifact, subject, and eligible state; got %v", pErr)
		}

		// Harmless rewording preserving the concrete git commit action in buildSt passes.
		rewordedEligible := "When requested by the user, stage explicit files and run `git commit -m MESSAGE -- PATHS`."
		if okErr := verifyPairedUsefulnessSection(
			"shared/units/git-mutation-user-consent.md", "self_now",
			buildSt.ID, specSt.ID,
			rewordedEligible, specRender,
			"git commit",
		); okErr != nil {
			t.Fatalf("harmless positive rewording failed: %v", okErr)
		}
	})
}
