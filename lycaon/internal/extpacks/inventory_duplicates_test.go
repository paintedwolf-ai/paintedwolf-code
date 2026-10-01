package extpacks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestInventoryRejectsDuplicateUnitIDsWithinPack(t *testing.T) {
	root := t.TempDir()
	for rel, body := range map[string]string{
		"agents/reviewer.md":   "reviewer prompt\n",
		"agents/reviewer.yaml": "name: reviewer\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir unit", os.MkdirAll(filepath.Dir(path), 0o700))
		testutil.FailErr(t, "write unit", os.WriteFile(path, []byte(body), 0o600))
	}
	_, err := InventoryPack(Pack{ID: "acme/duplicate", Root: OnDisk(root)}, Manifest{
		ID: "acme/duplicate", Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	})
	if err == nil || !strings.Contains(err.Error(), "agents/reviewer") {
		t.Fatalf("duplicate unit error = %v", err)
	}
}

func TestInventoryRejectsNestedWorkflowManifestWithSameID(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"workflows/release/workflow.yaml", "workflows/release/nested/workflow.yaml"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir workflow", os.MkdirAll(filepath.Dir(path), 0o700))
		testutil.FailErr(t, "write workflow", os.WriteFile(path, []byte("name: release\n"), 0o600))
	}
	_, err := InventoryPack(Pack{ID: "acme/duplicate", Root: OnDisk(root)}, Manifest{
		ID: "acme/duplicate", Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	})
	if err == nil || !strings.Contains(err.Error(), "workflows/release") {
		t.Fatalf("duplicate workflow error = %v", err)
	}
}
