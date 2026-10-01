package contract

import (
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

func TestAllBundledTemplatesComposeWithinPolicy(t *testing.T) {
	t.Parallel()
	c := workflowfixture.ContractWorkflowComposer(t)
	c.Templates = workflowfixture.ContractWorkflowTemplates(t)
	for id := range c.Templates {
		workflowfixture.ComposeTemplate(t, c, id)
	}
}

func TestTemplateComposeFeedbackSummaryMatchesManifest(t *testing.T) {
	t.Parallel()
	c := workflowfixture.ContractWorkflowComposer(t)
	c.Templates = workflowfixture.ContractWorkflowTemplates(t)
	for id := range c.Templates {
		result := workflowfixture.ComposeTemplate(t, c, id)
		key := "contract-" + id + "@1.0.0"
		bundled := workflowfixture.ContractBundledManifests(t)
		raw, err := workflowdef.ParseManifestYAML([]byte(result.EffectiveYAML))
		if err != nil {
			t.Fatalf("template %q effective yaml: %v", id, err)
		}
		merged := map[string]workflowdef.Manifest{}
		for k, v := range bundled {
			merged[k] = v
		}
		merged[key] = raw
		resolved, err := workflowdef.ResolveManifestChain(raw, merged)
		if err != nil {
			t.Fatalf("template %q resolve: %v", id, err)
		}
		workflowfixture.AssertFeedbackSummaryMatchesManifest(t, result.EffectiveSummary, resolved)
		workflowfixture.AssertDecisionSummaryMatchesManifest(t, result.EffectiveSummary, resolved)
	}
}
