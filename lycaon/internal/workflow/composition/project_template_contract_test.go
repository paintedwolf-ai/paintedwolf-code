package composition_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/vocabulary"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
)

func TestRequiredComposeGatesAreAvailableToProjectAuthors(t *testing.T) {
	for posture, gates := range testComposePolicy(t).PostureRequiredGates {
		for _, gate := range gates {
			if !vocabulary.IsOverlayLeaf(gate) {
				t.Errorf("%s requires unavailable project predicate %q", posture, gate)
			}
		}
	}
}

func TestEveryShippedWorkflowTemplateCanBePublished(t *testing.T) {
	templates := testTemplates(t)
	if len(templates) == 0 {
		t.Fatal("no shipped workflow templates")
	}
	for id := range templates {
		t.Run(id, func(t *testing.T) {
			ctx := t.Context()
			project := t.TempDir()
			p, store := testPersister(t)
			c := testComposer(t)
			c.SessionStore = store
			c.Templates = templates
			result, err := c.ComposeFromTemplate(ctx, workflowcomposition.ComposeFromTemplateRequest{
				SessionID: "s", ProjectDir: project, TemplateID: id, Params: map[string]any{"workflow_id": "template-example", "question": "Which project?"}, CreatedBy: workflowdrafts.Coordinator,
			})
			testutil.FailErr(t, "compose shipped template", err)
			_, err = p.Persist(ctx, workflowcomposition.PersistRequest{SessionID: "s", ProjectDir: project, WorkflowID: result.Summary.ID, Version: result.Summary.Version, Confirm: true, Trigger: "/template-example"})
			testutil.FailErr(t, "publish shipped template", err)
		})
	}
}
