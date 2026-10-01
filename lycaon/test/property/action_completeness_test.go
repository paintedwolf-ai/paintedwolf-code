package property

import (
	"testing"

	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/pkg/api"
	"pgregory.net/rapid"
)

func TestProjectLifecycleEvidenceNonEmptyForWorkflowBoundary(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		event := string(rapid.SampledFrom(api.AllWorkflowBoundaryKinds()).Draw(t, "event"))
		msg := api.Message{
			ID:   rapid.String().Draw(t, "id"),
			Role: api.MessageRoleSystem,
			Kind: api.MessageKindWorkflowBoundary,
			WorkflowBoundary: &api.WorkflowBoundaryMeta{
				Event: event,
				Phase: rapid.String().Draw(t, "phase"),
			},
		}
		rows := search.ProjectLifecycleEvidence("proj", "sess", msg)
		if len(rows) == 0 {
			t.Fatalf("event %q produced no evidence rows", event)
		}
	})
}
