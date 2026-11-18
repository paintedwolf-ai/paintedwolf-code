package contract

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

func TestComposePolicyYAMLLoads(t *testing.T) {
	t.Parallel()
	c := workflowfixture.ContractWorkflowComposer(t)
	compose := func(manifest []byte) error {
		_, err := c.Compose(t.Context(), workflowcomposition.ComposeRequest{
			SessionID: "contract-policy", ManifestYAML: manifest, CreatedBy: "coordinator", DryRun: true,
		})
		return err
	}
	contractcheck.FailErr(t, "control compose within policy", compose(composePolicyManifest(true, 0)))
	if codes := composeErrorCodes(t, compose(composePolicyManifest(false, 0))); !slices.Contains(codes, "extends_required") {
		t.Fatalf("compose without extends codes = %v want extends_required", codes)
	}
	overCap := composePolicyManifest(true, c.Policy.MaxPhasesCap())
	if codes := composeErrorCodes(t, compose(overCap)); !slices.Equal(codes, []string{"max_phases_exceeded"}) {
		t.Fatalf("compose over the phase cap codes = %v want [max_phases_exceeded]", codes)
	}
}

func TestBundledWorkflowTemplatesExpand(t *testing.T) {
	t.Parallel()
	catalog, err := workflowcomposition.LoadTemplatesFromDir(extpacks.Bundled(config.PlatformFlows.Join("_templates")))
	contractcheck.FailErr(t, "load workflow templates", err)
	if len(catalog) < 3 {
		t.Fatalf("templates = %d want >= 3", len(catalog))
	}
	for id, tmpl := range catalog {
		params := map[string]any{"workflow_id": "contract-" + id}
		if id == "clarify-then-implement-template" {
			params["question"] = "Which module?"
		}
		yaml, err := tmpl.Expand(params)
		if err != nil {
			t.Fatalf("%s expand: %v", id, err)
		}
		if len(yaml) == 0 {
			t.Fatalf("%s empty yaml", id)
		}
	}
}

// composePolicyManifest is a session compose that passes the bundled policy;
// extraPhases chains that many reachable phases after build.
func composePolicyManifest(extends bool, extraPhases int) []byte {
	var b strings.Builder
	b.WriteString("id: policy-session\nversion: 1.0.0\n")
	if extends {
		b.WriteString("extends: plan@1.0.0\n")
	}
	b.WriteString(`initial_posture: spec
agents:
  - { id: plan-writer, tools: profile }
  - { id: implementer, tools: profile }
phases:
  - id: research
    activity_label: Understanding the request
    gates:
      - hitl_consulted:research
    next: build
  - id: build
    activity_label: Building the change
    invoke_workflow:
      workflow_id: implement
      version: "1.0.0"
      blueprint: none
    complete_when: gates_satisfied
    gates:
      - child_run_complete
`)
	for i := range extraPhases {
		fmt.Fprintf(&b, "    next: extra-%d\n  - id: extra-%d\n    activity_label: Extra step\n", i, i)
	}
	return []byte(b.String())
}

func composeErrorCodes(t *testing.T, err error) []string {
	t.Helper()
	var failed *workflowcomposition.ComposeValidationFailed
	if !errors.As(err, &failed) {
		t.Fatalf("compose error = %v, want ComposeValidationFailed", err)
	}
	codes := make([]string, 0, len(failed.Errors))
	for _, e := range failed.Errors {
		codes = append(codes, e.Code)
	}
	return codes
}
