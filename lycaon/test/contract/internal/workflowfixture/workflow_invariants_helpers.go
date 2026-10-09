package workflowfixture

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func ContractWorkflowComposer(t *testing.T) *workflowcomposition.Composer {
	t.Helper()
	moduleRoot := filepath.Join(contractcheck.RepoRoot(t), "lycaon")
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	agents := orchestration.NewMemoryAgentRegistry()
	if err := orchestration.LoadRequiredAgentRegistry(context.Background(), agents); err != nil {
		contractcheck.FailErr(t, "agents.LoadRequiredAgentRegistry failed", err)
	}
	policy, err := workflowcomposition.LoadComposePolicy()
	contractcheck.FailErr(t, "workflowcomposition.LoadComposePolicy failed", err)
	return &workflowcomposition.Composer{
		ModuleRoot:   moduleRoot,
		SessionStore: workflowdrafts.NewMemory(),
		Registry:     reg,
		Agents:       agents,
		Policy:       policy,
	}
}

func ContractWorkflowTemplates(t *testing.T) workflowcomposition.TemplateCatalog {
	t.Helper()
	catalog, err := workflowcomposition.LoadTemplatesFromDir(extpacks.Bundled(config.PlatformFlows.Join("_templates")))
	contractcheck.FailErr(t, "load workflow templates", err)
	return catalog
}

func ContractBundledManifests(t *testing.T) map[string]workflowdef.Manifest {
	t.Helper()
	reg, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "workflow.RegistryFromDirs failed", err)
	return reg.All()
}

func contractResolvedTemplateManifests(t *testing.T) map[string]workflowdef.Manifest {
	t.Helper()
	bundled := ContractBundledManifests(t)
	templates := ContractWorkflowTemplates(t)
	out := map[string]workflowdef.Manifest{}
	for id, tpl := range templates {
		params := map[string]any{"workflow_id": "contract-" + id}
		if id == "clarify-then-implement-template" {
			params["question"] = "Contract test question?"
		}
		yaml, err := tpl.Expand(params)
		if err != nil {
			t.Fatalf("template %q expand: %v", id, err)
		}
		raw, err := workflowdef.ParseManifestYAML(yaml)
		if err != nil {
			t.Fatalf("template %q parse: %v", id, err)
		}
		parentCatalog := bundled
		merged := map[string]workflowdef.Manifest{}
		for k, v := range parentCatalog {
			merged[k] = v
		}
		merged[raw.ID+"@"+raw.Version] = raw
		resolved, err := workflowdef.ResolveManifestChain(raw, merged)
		if err != nil {
			t.Fatalf("template %q resolve: %v", id, err)
		}
		out["template:"+id] = resolved
	}
	return out
}

func ContractAllResolvedManifests(t *testing.T) map[string]workflowdef.Manifest {
	t.Helper()
	out := ContractBundledManifests(t)
	for k, v := range contractResolvedTemplateManifests(t) {
		out[k] = v
	}
	return out
}

func templateComposeParams(id string) map[string]any {
	params := map[string]any{"workflow_id": "contract-" + id}
	if id == "clarify-then-implement-template" {
		params["question"] = "Which API surface?"
	}
	return params
}

func ComposeTemplate(t *testing.T, c *workflowcomposition.Composer, templateID string) *workflowcomposition.ComposeResult {
	t.Helper()
	result, err := c.ComposeFromTemplate(context.Background(), workflowcomposition.ComposeFromTemplateRequest{
		SessionID:  "contract-session",
		TemplateID: templateID,
		Params:     templateComposeParams(templateID),
		CreatedBy:  "coordinator",
	})
	if err != nil {
		t.Fatalf("compose template %q: %v", templateID, err)
	}
	return result
}

func AssertFeedbackSummaryMatchesManifest(t *testing.T, summary api.ComposeEffectiveSummary, m workflowdef.Manifest) {
	t.Helper()
	for _, fb := range summary.Feedback {
		def, ok := m.PhaseByID(fb.ID)
		if !ok {
			t.Fatalf("feedback phase %q missing from manifest %s@%s", fb.ID, m.ID, m.Version)
		}
		if def.OnEnter.RequestUserFeedback == nil {
			t.Fatalf("phase %q in feedback_phases lacks request_user_feedback", fb.ID)
		}
		if strings.TrimSpace(fb.Prompt) == "" {
			t.Fatalf("feedback phase %q missing prompt in effective_summary", fb.ID)
		}
		if fb.Prompt != def.OnEnter.RequestUserFeedback.Prompt {
			t.Fatalf("feedback phase %q prompt mismatch: summary %q manifest %q", fb.ID, fb.Prompt, def.OnEnter.RequestUserFeedback.Prompt)
		}
	}
}

func AssertDecisionSummaryMatchesManifest(t *testing.T, summary api.ComposeEffectiveSummary, m workflowdef.Manifest) {
	t.Helper()
	for _, dec := range summary.Decisions {
		def, ok := m.PhaseByID(dec.ID)
		if !ok {
			t.Fatalf("decision phase %q missing from manifest %s@%s", dec.ID, m.ID, m.Version)
		}
		fb := def.OnEnter.RequestUserFeedback
		if fb == nil || !fb.ResolvedResponseType().IsChoice() {
			t.Fatalf("phase %q in decision_phases lacks a choice request_user_feedback", dec.ID)
		}
		if strings.TrimSpace(dec.Prompt) == "" {
			t.Fatalf("decision phase %q missing prompt in effective_summary", dec.ID)
		}
		if dec.Prompt != fb.Prompt {
			t.Fatalf("decision phase %q prompt mismatch: summary %q manifest %q", dec.ID, dec.Prompt, fb.Prompt)
		}
		wantOpts := fb.Options
		if len(dec.Options) != len(wantOpts) {
			t.Fatalf("decision phase %q options len = %d want %d", dec.ID, len(dec.Options), len(wantOpts))
		}
		for i := range wantOpts {
			if dec.Options[i] != wantOpts[i] {
				t.Fatalf("decision phase %q option[%d] = %q want %q", dec.ID, i, dec.Options[i], wantOpts[i])
			}
		}
	}
}

func HumanInputPhases(m workflowdef.Manifest) []workflowdef.PhaseDef {
	var out []workflowdef.PhaseDef
	for _, p := range m.PhaseDefs {
		if p.OnEnter.RequestUserFeedback != nil {
			out = append(out, p)
		}
	}
	return out
}
