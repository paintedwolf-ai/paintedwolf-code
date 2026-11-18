package composition_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
)

func TestPublishAuthoredWorkflowAndReopen(t *testing.T) {
	for _, tc := range []struct {
		name, leaf string
		reject     bool
	}{{"inherit", "phase_is:build", false}, {"bundled override", "gate_satisfied:plan_stub_valid", true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			project := t.TempDir()
			p, store := testPersister(t)
			c := testComposer(t)
			c.SessionStore = store
			c.Policy = nil
			p.Policy = nil
			const id = "custom-source"
			source := "id: " + id + "\nversion: 1.0.0\nextends: plan@1.0.0\nphases:\n  - id: research\n    activity_label: Researching\n    next: build\n    complete_when: " + tc.leaf + "\n"
			_, err := c.Compose(ctx, workflowcomposition.ComposeRequest{SessionID: "s", ManifestYAML: []byte(source), CreatedBy: workflowdrafts.Coordinator})
			testutil.FailErr(t, "compose workflow", err)
			draft, err := store.Get(ctx, "s", id, "1.0.0")
			testutil.FailErr(t, "read draft", err)
			if draft.AuthoredYAML != source || draft.ManifestYAML == source {
				t.Fatal("draft did not retain both source and resolved workflow")
			}
			result, err := p.Persist(ctx, workflowcomposition.PersistRequest{SessionID: "s", ProjectDir: project, WorkflowID: id, Version: "1.0.0", Trigger: "/custom-source", Confirm: true})
			if tc.reject {
				var failure *workflowcomposition.ComposeValidationFailed
				if !errors.As(err, &failure) || len(failure.Errors) == 0 || failure.Errors[0].Code != "domain_leaf_on_overlay" {
					t.Fatalf("persist error = %v", err)
				}
				retained, err := store.Get(ctx, "s", id, "1.0.0")
				testutil.FailErr(t, "recover rejected draft", err)
				if retained.AuthoredYAML != source {
					t.Fatal("rejected draft lost its source")
				}
				return
			}
			testutil.FailErr(t, "publish workflow", err)
			data, err := os.ReadFile(filepath.Join(project, result.Path))
			testutil.FailErr(t, "read published source", err)
			if !strings.Contains(string(data), "extends: plan@1.0.0") || strings.Contains(string(data), "research_satisfied") {
				t.Fatalf("published workflow lost its authored structure: %s", data)
			}
			resolver := workflowcatalog.Resolver{ProjectTierApplies: func(context.Context, string) bool { return true }}
			m, err := resolver.ForSession(ctx, project, "", id, "1.0.0")
			testutil.FailErr(t, "reopen project workflow", err)
			research, ok := m.PhaseByID("research")
			if !ok || research.CompleteWhen != tc.leaf || len(m.Phases) < 2 || m.Trigger != "/custom-source" {
				t.Fatalf("resolved workflow = %+v", m)
			}
		})
	}
}
