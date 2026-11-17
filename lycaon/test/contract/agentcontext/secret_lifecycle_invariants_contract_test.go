package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

// The contract runtime installs no secret resolver, so the served executor
// refuses with SECRET_REFERENCE_UNAVAILABLE exactly when it tries to resolve
// a reference and leaves the reference as literal text otherwise.
const (
	secretReference          = "{{paintedwolf-secret:9451ac87-2ef0-4647-b55d-92fda6921ec9}}"
	secretReferenceAttempted = "SECRET_REFERENCE_UNAVAILABLE"
)

func invokeWithSecretReference(t *testing.T, tool string, args map[string]any) error {
	t.Helper()
	host := toolfixture.ContractToolRuntime(t)
	_, err := host.Executor.Invoke(t.Context(), tool, args, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	return err
}

func resolutionAttempted(err error) bool {
	reject, ok := guidance.RefusalFromError(err)
	return ok && reject.Code() == secretReferenceAttempted
}

// File mutation tools resolve a reference in their declared content slot and
// never in the path.
func TestSecretFileMutationToolsDeclareFileReferenceSurface(t *testing.T) {
	for _, tc := range []struct {
		tool     string
		declared map[string]any
		path     map[string]any
	}{
		{"write",
			map[string]any{"path": "a.txt", "content": "TOKEN=" + secretReference},
			map[string]any{"path": secretReference, "content": "plain"}},
		{"edit",
			map[string]any{"path": "a.txt", "old_string": "x", "new_string": secretReference},
			map[string]any{"path": secretReference, "old_string": "x", "new_string": "y"}},
		{"replace_lines",
			map[string]any{"path": "a.txt", "operations": []any{map[string]any{"start_line": 1, "end_line": 1, "new_content": secretReference}}},
			map[string]any{"path": secretReference, "operations": []any{map[string]any{"start_line": 1, "end_line": 1, "new_content": "y"}}}},
		{"jq_edit",
			map[string]any{"path": "c.json", "query": ".k = $s", "vars": map[string]any{"s": secretReference}},
			map[string]any{"path": secretReference, "query": ".k = 1"}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			if err := invokeWithSecretReference(t, tc.tool, tc.declared); !resolutionAttempted(err) {
				t.Fatalf("reference in the declared slot was not resolved: %v", err)
			}
			if err := invokeWithSecretReference(t, tc.tool, tc.path); resolutionAttempted(err) {
				t.Fatalf("reference in the path was resolved: %v", err)
			}
		})
	}
}

// code_rewrite keeps a reference as literal text, where write resolves it.
func TestCodeRewriteExcludesSecretReferenceSurface(t *testing.T) {
	if err := invokeWithSecretReference(t, "write", map[string]any{"path": "a.txt", "content": secretReference}); !resolutionAttempted(err) {
		t.Fatalf("control: write did not resolve its reference: %v", err)
	}
	err := invokeWithSecretReference(t, "code_rewrite", map[string]any{
		"path": "a.go", "pattern": "x", "rewrite": secretReference,
	})
	if resolutionAttempted(err) {
		t.Fatalf("code_rewrite resolved a secret reference: %v", err)
	}
}

// command resolves a reference wherever it appears: in argv or in env.
func TestProcessToolsRetainUnrestrictedResolution(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"argv": {"command": "printf %s " + secretReference},
		"env":  {"command": "true", "env": map[string]any{"API_KEY": secretReference}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := invokeWithSecretReference(t, "command", args); !resolutionAttempted(err) {
				t.Fatalf("command did not resolve the reference in %s: %v", name, err)
			}
		})
	}
}
