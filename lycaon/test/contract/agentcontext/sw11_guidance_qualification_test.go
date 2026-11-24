package contract

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/watch"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func TestSW11GuidanceInventoryAndSurfaceOfferParity(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	engine := contractcheck.BundledPromptEngineForRoot(t)
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)

	// 1. Coordinator surface placement matrix (with and without folders) and compact upfront wire budget.
	measurements := measureToolSurfaces(t)
	investigateUpfront := measurements.Coordinator[toolcontract.SurfaceImplementInvestigate].Upfront
	if investigateUpfront <= 0 || investigateUpfront > 32_000 {
		t.Fatalf("implement_investigate upfront bytes = %d, want 1..32000", investigateUpfront)
	}

	planWithFolder, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: "implement_investigate"}, 1)
	contractcheck.FailErr(t, "compile implement_investigate with folder", err)
	if !slices.Contains(planWithFolder.ImmediateNames(), "watch") || slices.Contains(planWithFolder.ImmediateNames(), "watch_control") {
		t.Fatalf("idle implement_investigate must expose watch launcher upfront and hold watch_control for resource promotion, got immediate=%v", planWithFolder.ImmediateNames())
	}
	if !surface.AllowsWatchManagement("implement_investigate") || surface.AllowsWatchManagement("implement_synthesis") {
		t.Fatalf("unexpected watch_management surface gating: investigate=%v synthesis=%v",
			surface.AllowsWatchManagement("implement_investigate"), surface.AllowsWatchManagement("implement_synthesis"))
	}

	planNoFolder, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: "implement_investigate"}, 0)
	contractcheck.FailErr(t, "compile implement_investigate no folder", err)
	if slices.Contains(planNoFolder.ImmediateNames(), "command") || slices.Contains(planNoFolder.DeferredNames(), "command") {
		t.Fatalf("0-folder surface must omit command: immediate=%v deferred=%v", planNoFolder.ImmediateNames(), planNoFolder.DeferredNames())
	}

	// Verify locked web_research worker profile excludes watch and watch_control.
	for _, prof := range profiles {
		if prof.ID == "web_research" {
			if prof.Tools["watch"] || prof.Tools["watch_control"] {
				t.Fatalf("locked web_research profile must not include watch tools: %+v", prof.Tools)
			}
		}
	}

	// 2. Render command-jobs, process-finished, process-refused, and bundled skills with watch offered vs absent.
	roots := []projectroot.RootRef{{ID: "r1", Label: "repo", Path: "/workspace/repo", IsPrimary: true}}
	for _, tc := range []struct {
		name      string
		profileID string
		surfaceID string
		isCoord   bool
		wantWatch bool
	}{
		{name: "coordinator_investigate", profileID: prompts.CoordinatorProfileID, surfaceID: "implement_investigate", isCoord: true, wantWatch: true},
		{name: "coordinator_orchestrate", profileID: prompts.CoordinatorProfileID, surfaceID: "implement_routing", isCoord: true, wantWatch: true},
		{name: "worker_implement", profileID: "implement", isCoord: false, wantWatch: true},
		{name: "worker_web_research_restricted", profileID: "web_research", isCoord: false, wantWatch: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := prompts.ReaderInput{
				ProfileID: tc.profileID,
				Profiles:  profiles,
				RootCount: 1,
				HasGit:    true,
				WebSearch: true,
			}
			if tc.isCoord {
				plan, pErr := prompts.CompileSurfacePlan(tc.surfaceID, 1)
				contractcheck.FailErr(t, "compile surface plan", pErr)
				in.SurfaceID = tc.surfaceID
				in.Plan = plan
			}
			reader, rErr := prompts.NewReader(in)
			contractcheck.FailErr(t, "new reader", rErr)
			baseVars := prompts.AgentPromptSurfaceTemplateVars(prompts.AgentPromptSurface{
				SessionScratchDir: "/scratch/sess-1",
				Posture:           gate.PostureBalanced,
			})
			prompts.MergeWorkspaceRootsVars(baseVars, roots, "r1")
			vars, vErr := prompts.Vars(reader, baseVars)
			contractcheck.FailErr(t, "merge vars", vErr)
			if got, _ := vars["profile_has_watch"].(bool); got != tc.wantWatch {
				t.Fatalf("profile_has_watch = %v, want %v", got, tc.wantWatch)
			}

			vars["jobs"] = []map[string]any{
				{"handle": "cmd-1", "mode": "background", "origin_tool": "command", "elapsed": "3s", "watch_handle": "watch-1"},
			}
			vars["has_running"] = true
			vars["has_watched"] = tc.wantWatch
			vars["has_unwatched_wait"] = !tc.wantWatch
			jobsRendered, jErr := engine.Render(context.Background(), "guidance/command-jobs.md", vars)
			contractcheck.FailErr(t, "render command-jobs", jErr)
			if tc.wantWatch && !strings.Contains(jobsRendered, "watch_match") {
				t.Fatalf("expected watch_match guidance when watch is offered:\n%s", jobsRendered)
			}
			if !tc.wantWatch && strings.Contains(jobsRendered, "watch_control") {
				t.Fatalf("unexpected watch_control citation when watch is absent:\n%s", jobsRendered)
			}

			vars["command_completion"] = "handle=cmd-1 exit_code=0"
			finRendered, fErr := engine.Render(context.Background(), "guidance/coordinator-process-finished.md", vars)
			contractcheck.FailErr(t, "render coordinator-process-finished", fErr)
			if !strings.Contains(finRendered, "stopped_by_user") {
				t.Fatalf("expected stopped_by_user non-relaunch guidance:\n%s", finRendered)
			}

			vars["command_refusal"] = "handle=cmd-1 refused network"
			refRendered, rfErr := engine.Render(context.Background(), "guidance/coordinator-process-refused.md", vars)
			contractcheck.FailErr(t, "render coordinator-process-refused", rfErr)
			if !strings.Contains(refRendered, "sandbox refusal") {
				t.Fatalf("expected live refusal guidance:\n%s", refRendered)
			}
		})
	}

	// Verify shipped watch-touching skills and Android reference render cleanly within budgets.
	loadedSkills, diags := extpacks.LoadEffectiveSkills(extpacks.Active())
	if len(diags) > 0 {
		t.Fatalf("unexpected skill diagnostics: %+v", diags)
	}
	bySkill := map[string]skills.Skill{}
	for _, sk := range loadedSkills {
		bySkill[sk.Name] = sk
	}
	for _, name := range []string{"run-a-throwaway-service", "test-stripe-webhooks", "verify-a-change", "run-the-project-stack", "build-and-test-mobile-apps"} {
		sk, ok := bySkill[name]
		if !ok {
			t.Fatalf("missing bundled skill %q", name)
		}
		if len(sk.Body) == 0 || len(sk.Body) > skills.BundledBodyBudget {
			t.Fatalf("skill %q body bytes = %d outside 1..%d", name, len(sk.Body), skills.BundledBodyBudget)
		}
	}
	androidRef, err := bySkill["build-and-test-mobile-apps"].ReadResource("references/build_and_test_android.md")
	contractcheck.FailErr(t, "read android reference", err)
	if !strings.Contains(string(androidRef), "sys.boot_completed") {
		t.Fatalf("android reference must retain sys.boot_completed readiness requirement")
	}

	// 3. Exercise normal and rejected watch calls plus OAR policy rules and host-turn closeout invariants.
	sqlDB := testdbfixture.Open(t, "sw11-guidance.db")
	testdbseed.InsertSession(t, sqlDB, "sess-1", testdbseed.DefaultProjectID)
	watchStore := watch.NewStore(sqlDB, nil)
	catReg := toolfixture.RegisterCatalogToolsForContract(t)
	contractcheck.FailErr(t, "register watch tools", watch.RegisterWatchTools(catReg, watch.ToolDeps{
		Store: watchStore,
		ProfileConditions: map[string]map[string]bool{
			"coordinator": {"timer": true, "file_change": true, "git_ref": true},
			"implement":   {"timer": true, "file_change": true},
		},
		ProjectChat: func(_ context.Context, p string) bool { return p == testdbseed.DefaultProjectID },
	}))
	watchDef, ok := catReg.Definition("watch")
	if !ok {
		t.Fatal("missing watch tool in catalog registry")
	}
	okOut, err := watchDef.Handler(context.Background(), map[string]any{
		"conditions": []any{map[string]any{"kind": "timer", "delay_ms": 60000}},
	}, tools.ToolContext{
		Source:   tools.InvocationSource{Roots: roots, ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "coordinator", ProjectID: testdbseed.DefaultProjectID, SessionID: "sess-1", ToolCallID: "tc-ok"},
	})
	contractcheck.FailErr(t, "invoke normal watch tool", err)
	if !strings.Contains(okOut, "When you need this result, pass this condition to wait.") {
		t.Fatalf("watch receipt missing canonical wait hint: %s", okOut)
	}

	_, wakeErr := watchDef.Handler(context.Background(), map[string]any{
		"conditions": []any{map[string]any{"kind": "timer", "delay_ms": 60000}},
		"delivery":   "wake",
	}, tools.ToolContext{
		Source:   tools.InvocationSource{Roots: roots, ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement", WorkerJobID: "job-1", ProjectID: testdbseed.DefaultProjectID, SessionID: "sess-1"},
	})
	if wakeErr == nil || !strings.Contains(wakeErr.Error(), "WATCH_WAKE_NOT_ALLOWED") {
		t.Fatalf("expected WATCH_WAKE_NOT_ALLOWED, got %v", wakeErr)
	}

	_, draftErr := watchDef.Handler(context.Background(), map[string]any{
		"conditions": []any{map[string]any{"kind": "timer", "delay_ms": 60000}},
		"standing":   map[string]any{"name": "Worker draft", "prompt": "Disallowed on worker"},
	}, tools.ToolContext{
		Source:   tools.InvocationSource{Roots: roots, ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement", WorkerJobID: "job-1", ProjectID: testdbseed.DefaultProjectID, SessionID: "sess-1"},
	})
	if draftErr == nil || !strings.Contains(draftErr.Error(), "WATCH_DRAFT_INVALID") {
		t.Fatalf("expected WATCH_DRAFT_INVALID, got %v", draftErr)
	}

	// Verify all WATCH_* OAR policy rules load, reference declared facts, and pass conformance.
	contractcheck.FailErr(t, "install bundled anchors", anchorcatalog.InstallBundled())
	unreferenced, err := oar.AuditDeclarationReferences()
	contractcheck.FailErr(t, "audit OAR declaration references", err)
	if len(unreferenced) > 0 {
		t.Fatalf("unreferenced OAR host facts: %v", unreferenced)
	}
	loader, err := oar.NewLoader("")
	contractcheck.FailErr(t, "new OAR loader", err)
	policySet, err := loader.LoadEffectivePolicy()
	contractcheck.FailErr(t, "load effective OAR policy", err)
	for _, code := range []string{
		"WATCH_CAPACITY_REACHED",
		"WATCH_CONDITION_INVALID",
		"WATCH_DRAFT_DISMISSED",
		"WATCH_DRAFT_INVALID",
		"WATCH_EXPIRY_INVALID",
		"WATCH_HANDLE_UNKNOWN",
		"WATCH_REPEAT_UNSUPPORTED",
		"WATCH_TERMINAL",
		"WATCH_WAKE_NOT_ALLOWED",
		"COORDINATOR_HOST_TURN_REQUIRES_WAIT",
	} {
		rule, ok := policySet.Get(code)
		if !ok {
			t.Fatalf("missing OAR policy rule %q", code)
		}
		if rule.Effect != oar.EffectBlock {
			t.Fatalf("rule %q effect = %q, want block", code, rule.Effect)
		}
	}

	// Quiet watch collection on an ordinary synthesis-capable turn does NOT trigger COORDINATOR_HOST_TURN_REQUIRES_WAIT.
	closeoutRule, _ := policySet.Get("COORDINATOR_HOST_TURN_REQUIRES_WAIT")
	gc := oar.NewGuardContext()
	gc.Workflow.BatchClosed = false
	gc.Session.IsHostCycleTurn = false
	gc.Session.LastAssistant = "Checked the build and summarized the result."
	gc.Session.SurfaceMayFinish = true
	gc.Workers.WorkersIdle = true
	fired, err := oar.EvaluateCondition(closeoutRule.When, gc)
	contractcheck.FailErr(t, "evaluate closeout with quiet collection", err)
	if fired {
		t.Fatalf("quiet collection unexpectedly fired COORDINATOR_HOST_TURN_REQUIRES_WAIT")
	}

	// Host cycle turn with open progress steps and no tool call DOES trigger COORDINATOR_HOST_TURN_REQUIRES_WAIT.
	gc.Session.IsHostCycleTurn = true
	gc.Progress.ProgressHasOpenSteps = true
	firedOpen, err := oar.EvaluateCondition(closeoutRule.When, gc)
	contractcheck.FailErr(t, "evaluate host cycle closeout with open steps", err)
	if !firedOpen {
		t.Fatalf("expected COORDINATOR_HOST_TURN_REQUIRES_WAIT when host cycle has open steps")
	}

	// Run full shared OAR conformance corpus.
	corpusDir := oar.FindCorpusDir(testutil.CheckoutRoot(t))
	outcomes, err := oar.RunCorpusDir(context.Background(), corpusDir)
	contractcheck.FailErr(t, "run OAR corpus", err)
	for _, out := range outcomes {
		if out.Status != "pass" && out.Status != "skip" {
			t.Fatalf("OAR corpus fixture %s failed: %s", out.ID, out.Reason)
		}
	}
}
