package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCoordinatorSynthesisWrapupPromptAssembly(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corpus := renderCoordinatorTripartiteForRunContext(t, root,
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		"Summarize the batch",
		[]api.Message{
			{Role: api.MessageRoleUser, Content: "Build the feature"},
			{Role: api.MessageRoleAssistant, Content: "On it."},
		},
		"implement_synthesis",
	)
	for _, want := range []string{
		"Read-only report",
		"read-only",
		"`write`",
		"`edit`",
		"`task`",
		"Do not narrate fixes",
		"SYNTH_HANDLE_NOT_IN_LEGS",
		"SYNTH_CITATION_UNVERIFIABLE",
		"tool results in this chat",
		"Render when useful",
		"mockups or other visuals",
		"artifact_ids",
	} {
		if !strings.Contains(corpus, want) {
			t.Fatalf("synthesis wrapup prompt missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"Product edits → `task()`",
		"## Coordinator cycle",
		"Fan-out",
		"INVEST_HANDLE_NOT_OBSERVED",
		"escalate to `task(implementer",
	} {
		if strings.Contains(corpus, forbidden) {
			t.Fatalf("synthesis wrapup prompt must not contain orchestrate shell %q", forbidden)
		}
	}
	if surface.ExecutionModeFamily("implement_synthesis") != surface.ExecutionModeFamilyWrapup {
		t.Fatalf("implement_synthesis execution mode must be wrapup")
	}
}

func TestCoordinatorSynthesisSurfaceReadOnlyTools(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	synthesis := surfaces["implement_synthesis"]
	for _, forbidden := range []string{
		"write", "edit", "command", "task", "pack_board", "wait",
		"worker_cancel", "answer_decision", "extend_worker_budget", "decline_worker_budget",
		"promote_overlay", "reject_overlay", "preview_overlay",
	} {
		if contractcheck.ContainsString(synthesis, forbidden) {
			t.Fatalf("implement_synthesis must not expose %q", forbidden)
		}
	}
	if !contractcheck.ContainsString(synthesis, "update_progress") {
		t.Fatal("implement_synthesis must expose update_progress for checklist reconciliation")
	}
	for _, required := range []string{"read", "grep", "find", "list_dir", "scan_pack", "render_view"} {
		if !contractcheck.ContainsString(synthesis, required) {
			t.Fatalf("implement_synthesis missing survey tool %q", required)
		}
	}
}
