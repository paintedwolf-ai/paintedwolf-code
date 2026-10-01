package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestSecretFileMutationToolsDeclareFileReferenceSurface(t *testing.T) {
	t.Parallel()

	expectedFileTools := []string{"write", "edit", "replace_lines", "jq_edit"}
	for _, tool := range expectedFileTools {
		contract, ok := toolcontract.Lookup(tool)
		if !ok {
			t.Fatalf("missing catalog contract for %q", tool)
		}
		if contract.SecretReferenceSurface != toolcontract.SecretSurfaceFile {
			t.Errorf("tool %q has SecretReferenceSurface %q, want %q", tool, contract.SecretReferenceSurface, toolcontract.SecretSurfaceFile)
		}
		if !contract.SecretReferenceSurface.IsFile() {
			t.Errorf("tool %q SecretReferenceSurface.IsFile() is false", tool)
		}
		if len(contract.SecretReferenceArgs) == 0 {
			t.Errorf("tool %q has empty SecretReferenceArgs, want declared slots", tool)
		}
	}
}

func TestCodeRewriteExcludesSecretReferenceSurface(t *testing.T) {
	t.Parallel()

	contract, ok := toolcontract.Lookup("code_rewrite")
	if !ok {
		t.Fatal("missing catalog contract for code_rewrite")
	}
	if contract.AcceptsSecretReferences() {
		t.Errorf("code_rewrite must NOT accept secret references, got surface %q", contract.SecretReferenceSurface)
	}
}

func TestProcessToolsRetainUnrestrictedResolution(t *testing.T) {
	t.Parallel()

	for _, tool := range []string{"command", "terminal"} {
		contract, ok := toolcontract.Lookup(tool)
		if !ok {
			continue
		}
		if !contract.AcceptsSecretReferences() {
			continue
		}
		if len(contract.SecretReferenceArgs) != 0 {
			t.Errorf("process tool %q must not restrict SecretReferenceArgs: got %v", tool, contract.SecretReferenceArgs)
		}
	}
}
