package definition

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestShippedEffectiveManifestsRoundTripWithoutSemanticLoss(t *testing.T) {
	registry, err := RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)

	for key, original := range registry.All() {
		t.Run(key, func(t *testing.T) {
			raw, err := MarshalManifestYAML(original)
			testutil.FailErr(t, "MarshalManifestYAML", err)
			reloaded, err := ParseManifestYAML([]byte(raw))
			testutil.FailErr(t, "ParseManifestYAML", err)
			if diff := cmp.Diff(original, reloaded, cmpopts.EquateEmpty(), cmpopts.IgnoreFields(Manifest{}, "Sealed", "ArchiveDir")); diff != "" {
				t.Fatalf("effective manifest changed across persistence (-original +reloaded):\n%s\nyaml:\n%s", diff, raw)
			}
		})
	}
}

func TestInvocationGraphRejectedBeforeRegistryActivation(t *testing.T) {
	tests := []struct {
		name  string
		child string
		want  string
	}{
		{name: "missing target", want: "invokes unknown workflow child@1.0.0"},
		{
			name: "non-terminating child",
			child: `
id: child
version: 1.0.0
phases:
  - id: work
    activity_label: Working
    complete_when: gates_satisfied
    gates: [worker_cycle_ready]
    next: work
`,
			want: "child workflow has no terminal child path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := mustParseManifestForTest(t, `
id: parent
version: 1.0.0
phases:
  - id: execute
    activity_label: Executing
    invoke_workflow:
      workflow_id: child
      version: 1.0.0
      blueprint: none
    complete_when: gates_satisfied
    gates: [child_run_complete]
    next: done
  - id: done
    activity_label: Done
    terminal: true
`)
			catalog := map[string]Manifest{ManifestKey(parent.ID, parent.Version): parent}
			if tt.child != "" {
				child := mustParseManifestForTest(t, tt.child)
				catalog[ManifestKey(child.ID, child.Version)] = child
			}
			_, err := ResolveAllManifests(catalog)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("resolve error = %v want containing %q", err, tt.want)
			}
		})
	}
}

func TestInvokeWorkflowRequiresExplicitBlueprintMode(t *testing.T) {
	_, err := ParseManifestYAML([]byte(`
id: parent
version: 1.0.0
phases:
  - id: execute
    activity_label: Executing
    invoke_workflow:
      workflow_id: child
      version: 1.0.0
    complete_when: gates_satisfied
    gates: [child_run_complete]
`))
	if err == nil || !strings.Contains(err.Error(), "invoke_workflow.blueprint must be one of") {
		t.Fatalf("parse error = %v", err)
	}
}

func mustParseManifestForTest(t *testing.T, raw string) Manifest {
	t.Helper()
	manifest, err := ParseManifestYAML([]byte(raw))
	testutil.FailErr(t, "ParseManifestYAML", err)
	return manifest
}
