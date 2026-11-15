package contract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWorkerCopyBranchCoverageAndMissingFacts(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	engine := contractcheck.BundledPromptEngineForRoot(t)
	renderer := prompts.NewInjectRenderer(engine)

	roundBranches := map[string]bool{}
	for _, ws := range []string{"", "refreshed", "reset", "retained"} {
		for _, prior := range []string{"", "shelved", "open"} {
			for _, withInst := range []bool{false, true} {
				var inst []inject.WorkerMessageLine
				if withInst {
					inst = []inject.WorkerMessageLine{{Seq: 1, Body: "check"}}
				}
				block := inject.RenderWorkerRoundBlock(context.Background(), renderer, "s-br", inject.WorkerRoundFrame{
					Ordinal: 2, Brief: "goal", Instructions: inst, Workspace: ws, PriorChange: prior,
					EarlierResults: []string{"res-1"},
				})
				if block.Header == "" {
					t.Fatalf("empty worker round header for ws=%q prior=%q", ws, prior)
				}
				roundBranches[block.Header] = true
			}
		}
	}
	if len(roundBranches) < 8 {
		t.Fatalf("worker-round conditional branches collapsed to %d distinct renders", len(roundBranches))
	}

	msgBranches := map[string]bool{}
	for _, recipient := range []string{"coordinator", "worker"} {
		for _, needsReply := range []bool{false, true} {
			for _, more := range []bool{false, true} {
				block := inject.RenderWorkerMessagesBlock(context.Background(), renderer, "s-msg", recipient, []inject.WorkerMessageLine{
					{Worker: "w1", Seq: 1, ReplyToSeq: 1, NeedsReply: needsReply, Body: "note", ArtifactRef: "art-1"},
				}, more)
				parts := inject.WorkerMessagesParts(block, recipient)
				if len(parts) != 2 {
					t.Fatalf("expected 2 message parts for %s, got %d", recipient, len(parts))
				}
				msgBranches[block.Header] = true
			}
		}
	}
	if len(msgBranches) < 6 {
		t.Fatalf("worker-messages conditional branches collapsed to %d distinct renders", len(msgBranches))
	}

	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(engine))
	policyDirs, err := filepath.Glob(filepath.Join(root, "lycaon/config/packs/painted-wolf/*/policy"))
	contractcheck.FailErr(t, "glob policy dirs", err)
	results, err := oar.CheckRuleFiles(context.Background(), policyDirs, oar.RuleCheckOptions{
		SchemaDir: filepath.Join(root, "schemas"),
	})
	contractcheck.FailErr(t, "check OAR rules", err)
	for _, res := range results {
		if res.Scenarios == 0 {
			t.Errorf("OAR rule %s (%s) declares 0 scenarios", res.RuleID, res.Path)
		}
	}

	if vars, err := prompts.SkillSurfaceTemplateVars(prompts.SkillSurfaceVarsInput{}); err == nil || len(vars) != 0 {
		t.Fatalf("withheld ProfileID succeeded: vars=%v err=%v", vars, err)
	}
	outVars := map[string]any{}
	if err := prompts.MergeCoordinatorSurfacePathVars("", nil, outVars, prompts.SurfaceTurn{}); err == nil {
		t.Fatalf("withheld SurfaceID succeeded: vars=%v", outVars)
	}

	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	contractcheck.FailErr(t, "new OAR loader", err)
	rs, err := loader.LoadEffectivePolicy()
	contractcheck.FailErr(t, "load effective policy", err)
	checkedMissing := 0
	for _, rule := range rs.All() {
		if !strings.HasPrefix(rule.ID, "INSTRUCT_WORKER_") &&
			!strings.HasPrefix(rule.ID, "OVERLAY_SHELVE_") &&
			!strings.HasPrefix(rule.ID, "TASK_REVIEW_") &&
			rule.ID != "WORKER_CANCEL_TERMINAL" {
			continue
		}
		if rule.When != "" {
			matched, evalErr := oar.EvaluateCondition(rule.When, oar.NewGuardContext())
			if evalErr != nil || matched {
				t.Errorf("rule %s fired or errored without required facts: matched=%v err=%v", rule.ID, matched, evalErr)
			}
		}
		seenBindings := map[string]bool{}
		for _, field := range []string{
			rule.Copy.Title, rule.Copy.What, rule.Copy.Cause, rule.Copy.Why,
			rule.Copy.Fix, rule.Copy.Instead, rule.Copy.Message,
		} {
			bindings, parseErr := oarcore.ParseCopy(field)
			contractcheck.FailErr(t, "parse rule copy "+rule.ID, parseErr)
			for _, b := range bindings {
				if seenBindings[b.Name] {
					continue
				}
				seenBindings[b.Name] = true
				checkedMissing++
				rendered := oar.RenderRuleCopy(rule, map[string]any{b.Name: struct{}{}})
				if len(rendered) != 0 {
					t.Errorf("rule %s with invalid copy fact %s emitted copy %v", rule.ID, b.Name, rendered)
				}
			}
		}
	}
	if checkedMissing == 0 {
		t.Fatal("no worker rule copy bindings checked")
	}
}

func TestWorkerCopyLifetimesAndSkillHistory(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	standingPatterns := []string{
		"lycaon/config/packs/painted-wolf/*/agents/prompts/*.md",
		"lycaon/config/packs/painted-wolf/*/shared/partials/*.md",
	}
	for _, pat := range standingPatterns {
		files, err := filepath.Glob(filepath.Join(root, pat))
		contractcheck.FailErr(t, "glob standing templates", err)
		for _, file := range files {
			raw, err := os.ReadFile(file)
			contractcheck.FailErr(t, "read "+file, err)
			for _, v := range extractTemplateRootVars(string(raw)) {
				if volatilePromptVars[v] {
					t.Errorf("standing template %s reads volatile fact %q", filepath.Base(file), v)
				}
			}
		}
	}
	for _, sk := range stockSkillCorpus(t) {
		for _, v := range extractTemplateRootVars(sk.Body) {
			if volatilePromptVars[v] {
				t.Errorf("skill %s reads volatile fact %q", sk.Name, v)
			}
		}
		for _, rel := range sk.MarkdownResources() {
			body, err := sk.ReadResource(rel)
			contractcheck.FailErr(t, "read skill resource", err)
			for _, v := range extractTemplateRootVars(string(body)) {
				if volatilePromptVars[v] {
					t.Errorf("skill resource %s/%s reads volatile fact %q", sk.Name, rel, v)
				}
			}
		}
	}

	renderer := prompts.NewInjectRenderer(contractcheck.BundledPromptEngineForRoot(t))
	sessBuild := &api.Session{Posture: api.SessionPostureBuild}
	prefixBefore := renderCoordinatorTripartiteForRunContext(t, root, api.CoordinatorRunContext{}, sessBuild, surface.HostLoopWakeSentinel, nil, surface.SurfaceImplementDispatch)
	prefixAfter := renderCoordinatorTripartiteForRunContext(t, root, api.CoordinatorRunContext{}, sessBuild, surface.HostLoopWakeSentinel, WorkerCompletionHistory("implementer"), surface.SurfaceImplementDispatch)
	if prefixBefore != prefixAfter {
		t.Fatal("standing coordinator prefix changed across volatile worker completion")
	}

	tsWorking, err := inject.RenderTurnStateBlock(context.Background(), renderer, "s-life", inject.TurnStateInjectInput{
		Workers: []map[string]any{{"name": "w1", "agent": "implementer", "state": "working"}},
	})
	contractcheck.FailErr(t, "turn state working", err)
	tsAsking, err := inject.RenderTurnStateBlock(context.Background(), renderer, "s-life", inject.TurnStateInjectInput{
		Workers: []map[string]any{{"name": "w1", "agent": "implementer", "state": "asking", "question": "Use v2?"}},
	})
	contractcheck.FailErr(t, "turn state asking", err)
	if tsWorking == tsAsking {
		t.Fatal("turn-state frame did not reflect volatile worker transition")
	}

	prefixSynthesis := renderCoordinatorTripartiteForRunContext(t, root, api.CoordinatorRunContext{}, sessBuild, surface.HostLoopWakeSentinel, nil, surfaceImplementSynthesis)
	if prefixBefore == prefixSynthesis {
		t.Fatal("standing coordinator prefix did not re-render when slow surface changed")
	}

	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "compile tool plans", err)
	plan := plans[surface.SurfaceImplementDispatch]
	varsA, err := prompts.SkillSurfaceTemplateVars(prompts.SkillSurfaceVarsInput{
		ProfileID: prompts.CoordinatorProfileID, SurfaceID: surface.SurfaceImplementDispatch,
		Plan: plan, Offered: plan.AddressableNames(), HasGit: true, RootCount: 1,
	})
	contractcheck.FailErr(t, "skill vars A", err)
	for _, sk := range stockSkillCorpus(t) {
		if sk.Name != "orchestrate-a-large-task" {
			continue
		}
		before := renderSkillParts(t, sk, varsA)
		after := renderSkillParts(t, sk, varsA)
		for i := range before {
			if before[i].text != after[i].text {
				t.Fatalf("skill read part %s mutated across volatile state", before[i].label)
			}
		}
	}
}

func TestWorkerCopyAbsentStates(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	renderer := prompts.NewInjectRenderer(contractcheck.BundledPromptEngineForRoot(t))

	emptyTurnState, err := inject.RenderTurnStateBlock(context.Background(), renderer, "s-empty", inject.TurnStateInjectInput{})
	contractcheck.FailErr(t, "empty turn state", err)
	if emptyTurnState != "" {
		t.Fatalf("expected empty turn-state block with no workers or gates, got %q", emptyTurnState)
	}

	emptyMsgs := inject.RenderWorkerMessagesBlock(context.Background(), renderer, "s-empty", "worker", nil, false)
	if emptyMsgs.Header != "" || emptyMsgs.Messages != "" || inject.WorkerMessagesParts(emptyMsgs, "worker") != nil {
		t.Fatalf("expected empty message block when no messages exist, got %+v", emptyMsgs)
	}

	emptyRound := inject.RenderWorkerRoundBlock(context.Background(), renderer, "s-empty", inject.WorkerRoundFrame{Ordinal: 1})
	if emptyRound.Header != "" || emptyRound.Messages != "" || inject.WorkerRoundParts(emptyRound) != nil {
		t.Fatalf("expected empty round block when no brief or instructions exist, got %+v", emptyRound)
	}

	noCeiling := renderCoordinatorTripartiteForRunContext(t, root, api.CoordinatorRunContext{}, &api.Session{Posture: api.SessionPostureBuild}, "Inspect repo", nil, surface.SurfaceImplementDispatch)
	if strings.Contains(noCeiling, "[host:coordinator-spend-soft-stop]") {
		t.Fatal("unarmed spend ceiling rendered soft-stop banner")
	}

	archived := contractcheck.ReadRepoFile(t, root, "lycaon/config/packs/painted-wolf/security-survey/archive/security-survey/2.0.0/workflow.yaml")
	if strings.TrimSpace(archived) == "" {
		t.Fatal("archived workflow version missing")
	}
}
