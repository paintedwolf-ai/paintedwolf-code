package session_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/turnexecution"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type sessionTestPolicy struct {
	rules  *oar.RuleSet
	loader *oar.Loader
}

var loadSessionTestPolicy = sync.OnceValues(func() (sessionTestPolicy, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return sessionTestPolicy{}, fmt.Errorf("locate session test policy")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	loader, err := oar.NewLoader(filepath.Join(filepath.Dir(root), "schemas"))
	if err != nil {
		return sessionTestPolicy{}, err
	}
	rules, err := loader.LoadEffectivePolicy()
	return sessionTestPolicy{rules: rules, loader: loader}, err
})

func sessionTestOARPipeline(t *testing.T) *oar.GuardPipeline {
	t.Helper()
	policy, err := loadSessionTestPolicy()
	testutil.FailErr(t, "load session test policy", err)
	// Each host owns its counters, providers, and delivery sinks.
	p := oar.NewGuardPipeline(policy.rules, policy.loader, oar.NewCounterStore())
	for _, anchor := range []string{
		oar.AnchorCoordinatorPreInvoke,
		oar.AnchorCoordinatorCloseoutCheck,
		oar.AnchorCoordinatorPostTurn,
		oar.AnchorToolPreInvoke,
		oar.AnchorWorkerReportCheck,
		oar.AnchorToolPost,
	} {
		p.EnableAnchor(anchor)
	}
	return p
}

func evaluateHasCode(t *testing.T, gc *oar.GuardContext, code string, anchors ...string) bool {
	t.Helper()
	if gc == nil || code == "" {
		return false
	}
	p := sessionTestOARPipeline(t)
	if len(anchors) == 0 {
		anchors = []string{oar.AnchorCoordinatorPreInvoke, oar.AnchorCoordinatorCloseoutCheck, oar.AnchorWorkerReportCheck, oar.AnchorToolPreInvoke}
	}
	for _, anchor := range anchors {
		res, err := p.EvaluateBlock(context.Background(), anchor, gc)
		testutil.FailErr(t, "EvaluateBlock "+anchor, err)
		if res == nil {
			continue
		}
		if res.Decision == nil {
			continue
		}
		if res.Decision.Code == code {
			return true
		}
		for _, a := range res.Decision.Advisories {
			if a.Code == code {
				return true
			}
		}
	}
	return false
}

func TestWorkspaceRootEmptyWithoutBranch(t *testing.T) {
	task := &api.WorkerTask{
		WorkspacePath: "/primary",
		WorkspaceRoot: "",
	}
	if got := strings.TrimSpace(task.WorkspaceRoot); got != "" {
		t.Fatalf("WorkspaceRoot=%q want empty (not primary)", got)
	}
	if task.PrimaryRootPath() != "/primary" {
		t.Fatalf("PrimaryRootPath=%q", task.PrimaryRootPath())
	}
}

// A panic before the idle-restoring defer is registered still releases the busy session.
func TestRunTurnPanicInBusyWindowSelfHeals(t *testing.T) {
	client := llm.NewMockProvider(&llm.MockConfig{
		Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}},
	})
	fix := setupContextualToolsFixtureWithLLM(t, api.SessionPostureBuild, client)
	ctx := context.Background()

	turnexecution.RunTurnBusyWindowFaultForTest = func() { panic("boom: injected busy-window panic") }
	t.Cleanup(func() { turnexecution.RunTurnBusyWindowFaultForTest = nil })

	_, err := fix.Mgr.Submissions.Prompt(ctx, fix.Sess.ID, "hello")
	if err == nil {
		t.Fatal("Prompt returned no error; want the panic surfaced as a turn error, not swallowed")
	}

	got, getErr := fix.Store.Get(ctx, fix.Sess.ID)
	testutil.FailErr(t, "Get", getErr)
	if got.Status != api.SessionStatusIdle {
		t.Fatalf("status = %s want idle; a panic in the busy window must not leave the session stuck busy", got.Status)
	}

	// A second prompt proves recovery without restarting the manager.
	turnexecution.RunTurnBusyWindowFaultForTest = nil
	if _, err := fix.Mgr.Submissions.Prompt(ctx, fix.Sess.ID, "hello again"); err != nil {
		testutil.FailErr(t, "Prompt after recovery", err)
	}
}

func renderWorkerLegBlock(t *testing.T, ctx inject.WorkerLegContext) string {
	t.Helper()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	renderer := prompts.NewInjectRenderer(engine)
	block, err := inject.RenderWorkerLegInject(context.Background(), renderer, "sess-inject-test", ctx)
	testutil.FailErr(t, "inject.RenderWorkerLegInject failed", err)
	return block
}

const implementVerifyChecklist = "Run tests and the leg's external-state commands via command"

func TestChecklistOmitsL1Duplicate(t *testing.T) {
	process := []string{implementVerifyChecklist}
	checklist := []string{
		implementVerifyChecklist,
		"Branch on Code: from tool rejects",
	}
	filtered := assembly.FilterChecklistDedup(checklist, process)
	if len(filtered) != 1 {
		t.Fatalf("filtered = %v want one non-duplicate item", filtered)
	}
	if !strings.Contains(filtered[0], "Branch on Code") {
		t.Fatalf("filtered = %v", filtered)
	}
}

func TestBuildWorkerContextIncludesChecklist(t *testing.T) {
	ctx := inject.WorkerLegContext{
		LegID:           "leg-1",
		WorkflowID:      "default-pipeline",
		PhaseID:         "implement",
		TopologyPattern: "pipeline",
		Checklist: []string{
			implementVerifyChecklist,
			"Branch on Code: from tool rejects",
		},
	}
	block := renderWorkerLegBlock(t, ctx)
	if !strings.Contains(block, "## Playbook") || !strings.Contains(block, "1. Run tests") {
		t.Fatalf("block = %q", block)
	}
	if !strings.Contains(block, inject.WorkerLegInjectSentinel) {
		t.Fatalf("missing worker-leg sentinel in %q", block)
	}
}

func TestBuildWorkerContextIncludesCompletionCriteria(t *testing.T) {
	ctx := inject.WorkerLegContext{
		LegID:              "leg-2",
		CompletionCriteria: []string{"job:complete", "summary:present"},
		Checklist:          []string{"Branch on Code:"},
	}
	block := renderWorkerLegBlock(t, ctx)
	if !strings.Contains(block, "job:complete") || !strings.Contains(block, "summary:present") {
		t.Fatalf("block = %q", block)
	}
}

// The leg block lists what the agent's tool profile grants.
func TestLegToolsMatchAgentToolProfile(t *testing.T) {
	reg := orchestration.NewMemoryAgentRegistry()
	if err := orchestration.LoadRequiredAgentRegistry(context.Background(), reg); err != nil {
		testutil.FailErr(t, "LoadRequiredAgentRegistry", err)
	}
	agent, err := reg.Get(orchestration.ProfileImplementer)
	testutil.FailErr(t, "reg.Get failed", err)
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles failed", err)
	var granted []string
	for _, p := range profiles {
		if p.ID != agent.ToolProfile {
			continue
		}
		for name, ok := range p.Tools {
			if ok && !strings.HasSuffix(name, "*") {
				granted = append(granted, name)
			}
		}
	}
	if len(granted) == 0 {
		t.Fatalf("tool profile %q granted no tools", agent.ToolProfile)
	}
	block := renderWorkerLegBlock(t, inject.WorkerLegContext{LegTools: granted, Checklist: []string{"x"}})
	for _, tool := range granted {
		if !strings.Contains(block, "- "+tool) {
			t.Fatalf("missing tool %q in %q", tool, block)
		}
	}
}

func TestFormatWorkerContextIsolationFlag(t *testing.T) {
	block := renderWorkerLegBlock(t, inject.WorkerLegContext{
		RequiresIsolation: true,
		Checklist:         []string{"x"},
	})
	if !strings.Contains(block, "requires_isolation: true") {
		t.Fatalf("block = %q", block)
	}
}

func TestMatchPlaybookImplementerContract(t *testing.T) {
	contract, err := prompts.LoadPersonaContract()
	testutil.FailErr(t, "prompts.LoadPersonaContract failed", err)
	m, err := prompts.LoadPlaybookMatcherEffective(contract)
	testutil.FailErr(t, "prompts.LoadPlaybookMatcherEffective failed", err)
	got, err := m.MatchForAgent(orchestration.ProfileImplementer, "pipeline", "implement")
	testutil.FailErr(t, "m.MatchForAgent failed", err)
	if len(got) < 3 {
		t.Fatalf("checklist = %v", got)
	}
}

// knownWorkerEnvelopeStates is the complete completion-state vocabulary.
var knownWorkerEnvelopeStates = []string{
	"complete",
	"partial",
	"canceled",
	"open",
	"needs_decision",
	"held",
	"failed",
}

// deliberateStopStates end a leg without blocking wrap-up.
var deliberateStopStates = map[string]struct{}{
	string(api.WorkerSummaryStatusCanceled): {},
}

var repairOnlyStates = map[string]struct{}{
	string(api.WorkerSummaryStatusFailed): {},
}

func envelopeMessageForState(state string) api.Message {
	jobID := "j-" + state
	childSessionID := "child-" + state
	status := api.WorkerSummaryStatus(state)
	envelope := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID:          jobID,
		ChildSessionID: childSessionID,
		AgentType:      "implementer",
		State:          state,
	})
	return api.Message{
		Role: api.MessageRoleAssistant,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID:       jobID,
			ChildSessionID: childSessionID,
			AgentType:      "implementer",
			Status:         status,
			Envelope:       envelope,
		},
	}
}

func TestWorkerEnvelopeStateVocabularyFullyClassified(t *testing.T) {
	for _, state := range knownWorkerEnvelopeStates {
		msg := envelopeMessageForState(state)
		history := []api.Message{{Role: api.MessageRoleUser, Content: "do work"}, msg}
		since := api.UserIntentBoundary(history)

		blocks := workeroutcomes.OpenWorkerEnvelopeSince(history, since)
		finishedLeg := surface.TerminalWorkerCompletionMessage(msg)
		_, deliberateStop := deliberateStopStates[state]
		_, repairOnly := repairOnlyStates[state]

		buckets := 0
		for _, in := range []bool{blocks, finishedLeg, deliberateStop, repairOnly} {
			if in {
				buckets++
			}
		}
		if buckets != 1 {
			t.Fatalf("state %q lands in %d wrap-up buckets, want exactly 1 (blocks=%v, finishedLeg=%v, deliberateStop=%v, repairOnly=%v)",
				state, buckets, blocks, finishedLeg, deliberateStop, repairOnly)
		}
	}
}

var envelopeStateLiteralRE = regexp.MustCompile(`\bstate=["']([a-z_]+)["']`)

func TestWorkerEnvelopeStateLiteralsAreKnown(t *testing.T) {
	known := make(map[string]struct{}, len(knownWorkerEnvelopeStates))
	for _, s := range knownWorkerEnvelopeStates {
		known[s] = struct{}{}
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	lycaonRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	scanDirs := []string{
		filepath.Join(lycaonRoot, "internal", "session"),
		filepath.Join(lycaonRoot, "internal", "worker"),
		filepath.Join(lycaonRoot, "internal", "coordinator", "surface"),
	}

	var violations []string
	for _, dir := range scanDirs {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(lycaonRoot, path)
			for _, m := range envelopeStateLiteralRE.FindAllStringSubmatch(string(data), -1) {
				if _, ok := known[m[1]]; !ok {
					violations = append(violations, rel+`: state="`+m[1]+`" not in knownWorkerEnvelopeStates`)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	if len(violations) > 0 {
		t.Fatalf("unclassified worker-envelope state literals:\n  %s", strings.Join(violations, "\n  "))
	}
}
