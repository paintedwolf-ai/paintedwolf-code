package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

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
