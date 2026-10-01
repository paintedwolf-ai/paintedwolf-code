package contract

import (
	"path/filepath"
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// review_loop phases that spawn a Binding reviewer bind review_adjudicate so
// task() sees the workflow agents[] roster.
func TestReviewLoopDeliberationPhasesBindAdjudicateSurface(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cases := []struct {
		workflowID string
		phaseID    string
		surface    string
		template   string
	}{
		{"security-survey", "claims", "review_adjudicate", "agents/coordinator-surface-vet.md"},
		{"security-survey", "challenge", "review_adjudicate", "agents/coordinator-surface-vet.md"},
		{"options", "judge", "decision_adjudicate", "agents/coordinator-surface-orchestrate.md"},
	}
	for _, tc := range cases {
		t.Run(tc.workflowID+"/"+tc.phaseID, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", tc.workflowID, "workflows", tc.workflowID, "workflow.yaml")
			m, err := workflowdef.LoadManifestFromFile(path)
			contractcheck.FailErr(t, "LoadManifestFromFile "+tc.workflowID, err)
			ph, ok := m.PhaseByID(tc.phaseID)
			if !ok {
				t.Fatalf("missing phase %q", tc.phaseID)
			}
			if ph.ReviewLoop == nil {
				t.Fatalf("phase %q: review_loop required", tc.phaseID)
			}
			if ph.CoordinatorSurface != tc.surface {
				t.Fatalf("coordinator_surface = %q want %q", ph.CoordinatorSurface, tc.surface)
			}
			if ph.SurfaceTemplate != tc.template {
				t.Fatalf("surface_template = %q want %q", ph.SurfaceTemplate, tc.template)
			}
		})
	}
}
