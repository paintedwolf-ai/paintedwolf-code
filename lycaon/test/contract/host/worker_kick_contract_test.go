package contract

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWorkerTaskStartedKickImplementMode(t *testing.T) {
	t.Parallel()
	engine := contractcheck.BundledPromptEngineForRoot(t)
	out, err := engine.RenderKick(context.Background(), "worker-task-started", map[string]any{
		"agent_type": "path-explorer",
	})
	contractcheck.FailErr(t, "RenderKick worker-task-started", err)
	for _, forbid := range []string{
		"CompletionCriteria",
		"leg context block",
		"leg ``",
	} {
		if strings.Contains(out, forbid) {
			t.Fatalf("worker-task-started must not mention %q: %q", forbid, out)
		}
	}
	for _, want := range []string{
		"path-explorer",
		// The assignment is named by its pin, not its position: the context fit
		// re-emits pinned rows ahead of the retained window.
		"pinned in this context",
		"do not paste whole files",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("worker-task-started missing %q: %q", want, out)
		}
	}
}

func TestWorkerLegStartedKickWorkflowLeg(t *testing.T) {
	t.Parallel()
	engine := contractcheck.BundledPromptEngineForRoot(t)
	out, err := engine.RenderKick(context.Background(), "worker-leg-started", map[string]any{
		"agent_type": "implementer",
		"leg_id":     "leg-implement-1",
		"phase_id":   "implement",
	})
	contractcheck.FailErr(t, "RenderKick worker-leg-started", err)
	for _, want := range []string{
		"leg-implement-1",
		"implement",
		"CompletionCriteria",
		"leg context block",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("worker-leg-started missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "user message below") {
		t.Fatalf("worker-leg-started must not use task-spawn copy: %q", out)
	}
}

func TestWorkerCitationGroundingKickAdvertisesBriefBudget(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	engine := contractcheck.BundledPromptEngineForRoot(t)
	budget := loadCompactionWorkerSummaryBudget(t, root)
	// Render context mirrors boundCitationGroundingReport's kickData.
	out, err := engine.RenderKick(context.Background(), "worker-citation-grounding", map[string]any{
		"attempt":                  1,
		"max_attempts":             3,
		"worker_summary_max_chars": budget,
	})
	contractcheck.FailErr(t, "RenderKick worker-citation-grounding", err)
	if !strings.Contains(out, "**"+strconv.Itoa(budget)+"**") {
		t.Fatalf("worker-citation-grounding must advertise the brief budget %d: %q", budget, out)
	}
	if strings.Contains(out, "****") {
		t.Fatalf("worker-citation-grounding rendered an empty budget: %q", out)
	}
}

func TestWorkerKickTemplatesNoConditionalDualPath(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	kickDir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "security", "guidance")
	for _, name := range []string{"worker-leg-started.md", "worker-task-started.md"} {
		data, err := os.ReadFile(filepath.Join(kickDir, name))
		contractcheck.FailErr(t, "read kick template", err)
		text := string(data)
		if strings.Contains(text, "{% if") || strings.Contains(text, "{% else") {
			t.Fatalf("%s must not branch on spawn mode — use separate kick templates", name)
		}
	}
}
