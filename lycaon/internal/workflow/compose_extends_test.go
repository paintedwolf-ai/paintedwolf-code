package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestComposeExtendsUnknownParent422(t *testing.T) {
	c := testComposer(t)
	manifest := `id: orphan
version: 1.0.0
extends: missing@9.9.9
phases:
  - id: only
    activity_label: Test phase
    complete_when: plan_stub_valid
`
	_, err := c.Compose(context.Background(), ComposeRequest{SessionID: "s", ManifestYAML: []byte(manifest)})
	var vf *ComposeValidationFailed
	if !errors.As(err, &vf) {
		t.Fatalf("err = %v", err)
	}
	if vf.Errors[0].Code != "unknown_extends_parent" {
		t.Fatalf("code = %q", vf.Errors[0].Code)
	}
}

func TestComposeExtendsDepthExceeded(t *testing.T) {
	manifest := `id: deep
version: 1.0.0
extends: deep-1@1.0.0
phases:
  - id: only
    activity_label: Test phase
    complete_when: plan_stub_valid
`
	catalog := map[string]workflowdef.Manifest{
		workflowdef.ManifestKey("deep-1", "1.0.0"): {
			ID: "deep-1", Version: "1.0.0", Extends: "deep-2@1.0.0",
			PhaseDefs: []workflowdef.PhaseDef{{ID: "only", CompleteWhen: "plan_stub_valid"}},
		},
		workflowdef.ManifestKey("deep-2", "1.0.0"): {
			ID: "deep-2", Version: "1.0.0", Extends: "deep-3@1.0.0",
			PhaseDefs: []workflowdef.PhaseDef{{ID: "only", CompleteWhen: "plan_stub_valid"}},
		},
		workflowdef.ManifestKey("deep-3", "1.0.0"): {
			ID: "deep-3", Version: "1.0.0", Extends: "deep-4@1.0.0",
			PhaseDefs: []workflowdef.PhaseDef{{ID: "only", CompleteWhen: "plan_stub_valid"}},
		},
		workflowdef.ManifestKey("deep-4", "1.0.0"): {
			ID: "deep-4", Version: "1.0.0",
			PhaseDefs: []workflowdef.PhaseDef{{ID: "only", CompleteWhen: "plan_stub_valid"}},
		},
	}
	raw, err := workflowdef.ParseManifestYAML([]byte(manifest))
	testutil.FailErr(t, "ParseManifestYAML failed", err)
	_, err = workflowdef.ResolveManifestChain(raw, catalog)
	if err == nil {
		t.Fatal("expected depth error")
	}
	errs := extendsChainError(err)
	if errs[0].Code != "extends_depth_exceeded" {
		t.Fatalf("code = %q", errs[0].Code)
	}
}
