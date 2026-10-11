package contract

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestCoordinatorAskUserInvariantCoordinatorProfileHasAskUser(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "tools", "profiles", "coordinator.yaml"))
	contractcheck.FailErr(t, "read coordinator profile", err)
	var doc struct {
		Tools map[string]string `yaml:"tools"`
	}
	contractcheck.FailErr(t, "parse coordinator profile", yaml.Unmarshal(raw, &doc))
	if doc.Tools["ask_user"] != "sticky" {
		t.Fatalf("ask_user sticky on coordinator = %q", doc.Tools["ask_user"])
	}
}

func TestCoordinatorAskUserInvariantWorkerProfilesLackAskUser(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	profilesDir := filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "tools", "profiles")
	entries, err := os.ReadDir(profilesDir)
	contractcheck.FailErr(t, "read profiles", err)
	for _, ent := range entries {
		name := ent.Name()
		if !strings.HasSuffix(name, ".yaml") || name == "coordinator.yaml" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(profilesDir, name))
		contractcheck.FailErr(t, "read profile "+name, err)
		if strings.Contains(string(raw), "ask_user:") {
			t.Fatalf("worker/other profile %s must not list ask_user", name)
		}
	}
}

func TestCoordinatorAskUserInvariantSurfacesIncludeAskUser(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	for _, id := range []string{
		toolcontract.SurfaceImplementInvestigate,
		toolcontract.SurfaceImplementDispatch,
		"implement_overlay_promote",
		"implement_park",
		"implement_routing",
		"workflow_compose",
		"orchestrate_plan",
		// Gated authoring phases retain a human-fork channel.
		"recon_reconcile",
		"plan_research",
		"plan_stub",
		"plan_review",
		"plan_execute",
	} {
		if !contractcheck.ContainsString(surfaces[id], "ask_user") {
			t.Fatalf("surface %s missing ask_user", id)
		}
	}
}

func TestCoordinatorAskUserInvariantSyntheticPhasePrefix(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	// Ask ids share the host phase prefix.
	raw, err := os.ReadFile(filepath.Join(lycaonRoot, "internal", "workflow", "inputs", "asks.go"))
	contractcheck.FailErr(t, "read inputs/asks.go", err)
	if !strings.Contains(string(raw), `askUserPhasePrefix = "ask-"`) {
		t.Fatal("ask_user must mint phase ids with ask- prefix")
	}
}

func TestCoordinatorAskUserInvariantNoNewMessageKind(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	kindFile := filepath.Join(lycaonRoot, "pkg", "api", "session_types.go")
	raw, err := os.ReadFile(kindFile)
	contractcheck.FailErr(t, "read session_types", err)
	if strings.Contains(string(raw), "coordinator_ask") {
		t.Fatal("must not introduce coordinator_ask MessageKind")
	}
	if !strings.Contains(string(raw), "MessageKindWorkflowFeedback") {
		t.Fatal("ask_user must reuse MessageKindWorkflowFeedback")
	}
}

func TestCoordinatorAskUserInvariantDenReusesWorkflowFeedbackCard(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	t.Parallel()
	denRoot := filepath.Join(root, "lycaon-den", "src")
	card := filepath.Join(denRoot, "components", "workflow", "WorkflowFeedbackCard.tsx")
	if _, err := os.Stat(card); err != nil {
		t.Fatalf("WorkflowFeedbackCard.tsx missing: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(denRoot, "components"))
	contractcheck.FailErr(t, "read den components", err)
	for _, ent := range entries {
		n := strings.ToLower(ent.Name())
		if strings.Contains(n, "askuser") || strings.Contains(n, "ask_user") {
			t.Fatalf("unexpected ask_user Den component: %s", ent.Name())
		}
	}
}

func TestCoordinatorAskUserInvariantOnePendingRejectCode(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(lycaonRoot, "internal", "workflow", "inputs", "asks.go"))
	contractcheck.FailErr(t, "read inputs/asks.go", err)
	if !strings.Contains(string(raw), `ASK_USER_ALREADY_PENDING`) {
		t.Fatal("second ask while pending must reject ASK_USER_ALREADY_PENDING")
	}
}

func TestCoordinatorAskUserInvariantInjectDtoHasNoLastUserAskResponse(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(lycaonRoot, "internal", "coordinator", "inject", "inject_dto.go"))
	contractcheck.FailErr(t, "read inject_dto", err)
	if strings.Contains(string(raw), "LastUserAskResponse") {
		t.Fatal("deleted last_user_ask_response inject — answer is on ask_user tool row")
	}
}

func TestCoordinatorAskUserInvariantNoLastUserAskResponseScaffoldLatch(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	for _, rel := range []string{
		filepath.Join("internal", "workflow", "inputs", "asks.go"),
		filepath.Join("internal", "workflow", "inputs", "feedback.go"),
		filepath.Join("internal", "api", "harness_control.go"),
	} {
		raw, err := os.ReadFile(filepath.Join(lycaonRoot, rel))
		contractcheck.FailErr(t, "read "+rel, err)
		if strings.Contains(string(raw), "last_user_ask_response") {
			t.Fatalf("%s must not keep last_user_ask_response", rel)
		}
	}
}

func TestCoordinatorAskUserInvariantParkOnAskAndAnswerRewrite(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(lycaonRoot, "internal", "coordinator", "loopwake", "wait_completion.go"))
	contractcheck.FailErr(t, "read wait completion", err)
	park, err := os.ReadFile(filepath.Join(lycaonRoot, "internal", "coordinator", "loopwake", "wait_lifecycle.go"))
	contractcheck.FailErr(t, "read wait lifecycle", err)
	if !strings.Contains(string(raw), "AskUserEndsCycle") || !strings.Contains(string(park), "ParkForPendingUserInput") {
		t.Fatal("loopwake must park-on-ask (AskUserEndsCycle + ParkForPendingUserInput)")
	}
	ans, err := os.ReadFile(filepath.Join(lycaonRoot, "internal", "workflow", "inputs", "cards.go"))
	contractcheck.FailErr(t, "read inputs/cards", err)
	if !strings.Contains(string(ans), "PersistCoordinatorAskAnswer") || !strings.Contains(string(ans), "PersistAskUserAnswerForToolCall") {
		t.Fatal("coordinator ask result projection required")
	}
	ui, err := os.ReadFile(filepath.Join(lycaonRoot, "internal", "workflow", "inputs", "feedback.go"))
	contractcheck.FailErr(t, "read inputs/feedback", err)
	if !strings.Contains(string(ui), "PersistCoordinatorAskAnswer") {
		t.Fatal("resolve paths must project the authoritative coordinator ask result")
	}
	projection, err := os.ReadFile(filepath.Join(lycaonRoot, "internal", "workflow", "inputs", "ask_projection.go"))
	contractcheck.FailErr(t, "read ask projection", err)
	if !strings.Contains(string(projection), "PersistCoordinatorAskAnswer") {
		t.Fatal("reconciliation must project the authoritative coordinator ask result")
	}
}

func TestCoordinatorAskUserInvariantAskUserToolNoCheckpointKind(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	for _, rel := range []string{
		filepath.Join("internal", "workflow", "inputs", "ask_tool.go"),
		filepath.Join("internal", "workflow", "inputs", "asks.go"),
	} {
		raw, err := os.ReadFile(filepath.Join(lycaonRoot, rel))
		contractcheck.FailErr(t, "read "+rel, err)
		src := string(raw)
		for _, needle := range []string{
			"RequestCheckpoint",
			"CheckpointKind",
			"hitl.",
		} {
			if strings.Contains(src, needle) {
				t.Fatalf("%s must not reference %q (ask_user stays on feedback bus)", rel, needle)
			}
		}
	}
}

func TestCoordinatorAskUserInvariantNoProseBypassOnAskUserTool(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(lycaonRoot, "internal", "workflow", "inputs", "ask_tool.go"))
	contractcheck.FailErr(t, "read inputs/ask_tool.go", err)
	src := string(raw)
	for _, needle := range []string{
		"TryResolveUserFeedback",
		"ResolveUserFeedback",
		"ResolveUserDecision",
	} {
		if strings.Contains(src, needle) {
			t.Fatalf("ask_user tool must not call %s (prose/auto-satisfy bypass)", needle)
		}
	}
}

func TestCoordinatorAskUserInvariantKickFeedbackReceivedRegistered(t *testing.T) {
	t.Parallel()
	reg := catalogfixture.LoadInformBindings(t)
	b, ok := reg.Inform(anchor.FeedbackReceived)
	if !ok || b == nil || b.Render != "coordinator-feedback-received" {
		t.Fatal("feedback.received inform Binding missing or wrong render")
	}
	if b.Invariants.TriggerClass == "" {
		t.Fatal("feedback.received Binding missing invariants.trigger_class")
	}
}

func TestCoordinatorAskUserInvariantFeedbackReceivedWakesWait(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	subRaw, err := os.ReadFile(filepath.Join(lycaonRoot, "internal", "coordinator", "loopwake", "subscription.go"))
	contractcheck.FailErr(t, "read subscription.go", err)
	if !strings.Contains(string(subRaw), "anchor.FeedbackReceived") {
		t.Fatal("KickFeedbackReceived must be always-wake so wait() resumes on answer")
	}
}

func TestCoordinatorAskUserInvariantPendingUserInputBlocksTask(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	hintPath := filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "policy", "PENDING_USER_INPUT_TASK_FORBIDDEN.yaml")
	if _, err := os.Stat(hintPath); err != nil {
		t.Fatalf("PENDING_USER_INPUT_TASK_FORBIDDEN hint missing: %v", err)
	}
	guardRaw, err := os.ReadFile(filepath.Join(lycaonRoot, "internal", "coordinator", "guard", "pending_user_input_task_guard.go"))
	contractcheck.FailErr(t, "read pending_user_input_task_guard.go", err)
	if !strings.Contains(string(guardRaw), `PendingUserInputTaskForbiddenCode = "PENDING_USER_INPUT_TASK_FORBIDDEN"`) {
		t.Fatal("task-while-pending guard code missing")
	}
}

func TestCoordinatorAskUserInvariantNoParallelVisualReviewTool(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	dir := filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "tools", "schemas")
	entries, err := os.ReadDir(dir)
	contractcheck.FailErr(t, "read tools/schemas", err)
	for _, ent := range entries {
		name := strings.TrimSuffix(ent.Name(), ".yaml")
		if name == "visual_review" || name == "approve_mockup" {
			t.Fatalf("must not register parallel tool %q", name)
		}
	}
}

func TestCoordinatorAskUserInvariantCapturePageRequestableOnInvestigateAndAllowedByCoordinator(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: toolcontract.SurfaceImplementInvestigate}, 1)
	contractcheck.FailErr(t, "compile investigate tool plan", err)
	requestable := plan.DeferredNames()
	for _, tool := range []string{"capture_page", "render_view"} {
		if !contractcheck.ContainsString(requestable, tool) {
			t.Fatalf("implement_investigate requestable surface missing %s", tool)
		}
	}
	profile, err := os.ReadFile(filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "tools", "profiles", "coordinator.yaml"))
	contractcheck.FailErr(t, "read coordinator profile", err)
	src := string(profile)
	for _, tool := range []string{"capture_page:", "render_view:"} {
		if !strings.Contains(src, tool) {
			t.Fatalf("coordinator profile missing %s", tool)
		}
	}
}

func TestCoordinatorAskUserInvariantRequestDecisionArtifactIdInSchema(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "tools", "schemas", "request_decision.yaml"))
	contractcheck.FailErr(t, "read request_decision schema", err)
	src := string(raw)
	if !strings.Contains(src, "artifact_id:") {
		t.Fatal("request_decision schema must include artifact_id")
	}
	if !strings.Contains(src, "artifact_ids:") {
		t.Fatal("request_decision schema must include artifact_ids for compare relay")
	}
}

func TestCoordinatorAskUserInvariantAskUserArtifactsInSchema(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "tools", "schemas", "ask_user.yaml"))
	contractcheck.FailErr(t, "read ask_user schema", err)
	src := string(raw)
	if !strings.Contains(src, "artifacts:") {
		t.Fatal("ask_user schema must include artifacts[]")
	}
	for _, gone := range []string{"artifact_id:", "artifact_ids:", "allow_other:", "blocking:", "default_response:"} {
		if strings.Contains(src, gone) {
			t.Fatalf("ask_user schema must not include %q", gone)
		}
	}
	if !strings.Contains(src, "    secret:\n") || !strings.Contains(src, "        purpose:\n") {
		t.Fatal("ask_user secret metadata must include purpose")
	}
}

func TestCoordinatorAskUserInvariantAnswerDecisionResolvedByInSchema(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "tools", "schemas", "answer_decision.yaml"))
	contractcheck.FailErr(t, "read answer_decision schema", err)
	src := string(raw)
	if !strings.Contains(src, "resolved_by:") {
		t.Fatal("answer_decision schema must include resolved_by")
	}
}

func TestCoordinatorAskUserInvariantCompareSynthesizesLettersNotNeither(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(lycaonRoot, "internal", "workflow", "inputs", "ask_grammar.go"))
	contractcheck.FailErr(t, "read inputs/ask grammar", err)
	src := string(raw)
	if !strings.Contains(src, "synthesizeCompareOptions") {
		t.Fatal("compare must synthesize options via synthesizeCompareOptions")
	}
	if strings.Contains(src, "Neither") || strings.Contains(src, "askUserCompareNeither") {
		t.Fatal("compare must not synthesize Neither — Other+text is the escape hatch")
	}
	if strings.Contains(src, "Approve all") {
		t.Fatal("compare must not ship Approve-all gallery options")
	}
}

func TestCoordinatorAskUserInvariantHasPendingUserInputIgnoresWorkerDecisions(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	// Workflow feedback reads scaffold state only.
	raw, err := os.ReadFile(filepath.Join(lycaonRoot, "internal", "scaffoldvars", "scaffold.go"))
	contractcheck.FailErr(t, "read scaffoldvars", err)
	src := string(raw)
	for _, needle := range []string{"DecisionStore", "request_decision", "PendingDecision"} {
		if strings.Contains(src, needle) {
			t.Fatalf("HasPendingUserInput must not reference %q (latch separation)", needle)
		}
	}
}

func TestCoordinatorAskUserInvariantNoToolAskExpiryHelpers(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(lycaonRoot, "internal", "workflow", "inputs", "asks.go"))
	contractcheck.FailErr(t, "read inputs/asks", err)
	src := string(raw)
	for _, needle := range []string{"ExpirePendingToolAsks", "scheduleToolAskExpiry", "ask_user_timeout"} {
		if strings.Contains(src, needle) {
			t.Fatalf("RequestUserInput must not reference %q", needle)
		}
	}
}

func TestCoordinatorAskUserInvariantDisciplineNoFailOpenTeaching(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(
		lycaonRoot, "config", "packs", "painted-wolf", "hitl", "shared", "partials", "coordinator-ask-user-discipline.md",
	))
	contractcheck.FailErr(t, "read ask_user discipline", err)
	src := string(raw)
	lower := strings.ToLower(src)
	for _, needle := range []string{"blocking: false", "ask_user_timeout", "blocking: true"} {
		if strings.Contains(lower, needle) {
			t.Fatalf("discipline teaches %q, which ask_user does not accept", needle)
		}
	}
	// Match affirmative countdown and fail-open guidance.
	if strings.Contains(lower, "shows remaining") || strings.Contains(lower, "card shows remaining") {
		t.Fatal("discipline must not teach open-card countdown")
	}
	if !strings.Contains(lower, "composer") {
		t.Fatal("discipline must teach composer / Den answer path")
	}
}

func TestCoordinatorAskUserInvariantDenOpenAskHasNoCountdownUi(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	t.Parallel()
	denRoot := filepath.Join(root, "lycaon-den", "src")
	for _, rel := range []string{
		"components/workflow/AskUserDock.tsx",
		"components/workflow/WorkflowFeedbackCard.tsx",
	} {
		raw, err := os.ReadFile(filepath.Join(denRoot, rel))
		contractcheck.FailErr(t, "read "+rel, err)
		src := string(raw)
		for _, needle := range []string{
			"workflow-feedback-remaining",
			"workflowFeedbackRemainingLabel",
			"Time remaining",
		} {
			if strings.Contains(src, needle) {
				t.Fatalf("%s must not render countdown chrome (%q)", rel, needle)
			}
		}
	}
	card, err := os.ReadFile(filepath.Join(denRoot, "components/workflow/WorkflowFeedbackCard.tsx"))
	contractcheck.FailErr(t, "read WorkflowFeedbackCard", err)
	if !strings.Contains(string(card), "workflow-feedback-open-marker") {
		t.Fatal("open transcript path must render workflow-feedback-open-marker")
	}
	if strings.Contains(string(card), "workflow-feedback-other") || strings.Contains(string(card), "workflow-feedback-text") {
		t.Fatal("open card must not keep card-local Other/text textarea")
	}
}
