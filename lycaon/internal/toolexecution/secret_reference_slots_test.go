package toolexecution

import (
	"errors"
	"reflect"
	"testing"
)

const slotReference = "{{paintedwolf-secret:9451ac87-2ef0-4647-b55d-92fda6921ec9}}"

// resolves reports whether dispatch tries to resolve a reference in args for
// tool's catalog contract. With no resolver installed an attempt surfaces as
// errSecretReferenceUnavailable; otherwise the arguments pass through as text.
func resolves(t *testing.T, tool string, args map[string]any) bool {
	t.Helper()
	got, err := NewExecutor(nil, nil, "").Secrets.resolveSecretReferences(t.Context(), tool, args, invokedAs(catalogContract(t, tool)))
	if errors.Is(err, errSecretReferenceUnavailable) {
		return true
	}
	if err != nil {
		t.Fatalf("%s: resolution error %v", tool, err)
	}
	if !reflect.DeepEqual(got.Arguments, args) {
		t.Fatalf("%s: literal arguments changed: %#v", tool, got.Arguments)
	}
	return false
}

// File mutation operations resolve a reference in their declared content slot
// and never in the path.
func TestSecretFileMutationToolsDeclareFileReferenceSurface(t *testing.T) {
	for _, tc := range []struct {
		tool           string
		declared, path map[string]any
	}{
		{"write",
			map[string]any{"path": "a.txt", "content": "TOKEN=" + slotReference},
			map[string]any{"path": slotReference, "content": "plain"}},
		{"edit",
			map[string]any{"path": "a.txt", "old_string": "x", "new_string": slotReference},
			map[string]any{"path": slotReference, "old_string": "x", "new_string": "y"}},
		{"replace_lines",
			map[string]any{"path": "a.txt", "operations": []any{map[string]any{"start_line": 1, "end_line": 1, "new_content": slotReference}}},
			map[string]any{"path": slotReference, "operations": []any{map[string]any{"start_line": 1, "end_line": 1, "new_content": "y"}}}},
		{"jq_edit",
			map[string]any{"path": "c.json", "query": ".k = $s", "vars": map[string]any{"s": slotReference}},
			map[string]any{"path": slotReference, "query": ".k = 1"}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			if !resolves(t, tc.tool, tc.declared) {
				t.Fatal("reference in the declared slot was not resolved")
			}
			if resolves(t, tc.tool, tc.path) {
				t.Fatal("reference in the path was resolved")
			}
		})
	}
}

// code_rewrite keeps a reference as literal text in the slot where write
// resolves it.
func TestCodeRewriteExcludesSecretReferenceSurface(t *testing.T) {
	if !resolves(t, "write", map[string]any{"path": "a.txt", "content": slotReference}) {
		t.Fatal("control: write did not resolve its reference")
	}
	if resolves(t, "code_rewrite", map[string]any{"path": "a.go", "content": slotReference, "rewrite": slotReference}) {
		t.Fatal("code_rewrite resolved a secret reference")
	}
}

// command resolves a reference wherever it appears in its arguments.
func TestProcessToolsRetainUnrestrictedResolution(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"argv":   {"command": "printf %s " + slotReference},
		"nested": {"command": "true", "options": map[string]any{"value": []any{slotReference}}},
	} {
		if !resolves(t, "command", args) {
			t.Fatalf("command did not resolve the reference in %s", name)
		}
	}
}
