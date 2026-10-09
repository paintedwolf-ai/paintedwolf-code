package survey

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

// holdMetadataLane occupies the catalog's traversal lane for one root, so a
// build for it cannot be admitted until the returned release runs.
func holdMetadataLane(t *testing.T, root string) func() {
	t.Helper()
	release, err := backgroundwork.Process().Acquire(t.Context(), backgroundwork.Request{
		Key: "survey-inventory-test", Lane: root, Priority: backgroundwork.PriorityInteractive,
		Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata},
	})
	if err != nil {
		testutil.FailErr(t, "hold the metadata lane", err)
	}
	return release
}

func TestFindRejectsWhileTheCatalogHasNoGeneration(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
	release := holdMetadataLane(t, root)
	defer release()

	tool := &FindTool{Boundary: nativefixture.Boundary(t), Catalog: sourcecatalog.New()}
	_, err := tool.Run(context.Background(), map[string]any{"name_glob": "**/*.go"}, nativefixture.Context(root))
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "SURVEY_INVENTORY_WARMING" {
		t.Fatalf("find error = %v, want SURVEY_INVENTORY_WARMING", err)
	}
}

func TestFindServesTheLastCompleteGenerationWhileItRebuilds(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
	catalog := sourcecatalog.New()
	tool := &FindTool{Boundary: nativefixture.Boundary(t), Catalog: catalog}
	args := map[string]any{"name_glob": "**/*.go"}

	out, err := tool.Run(context.Background(), args, nativefixture.Context(root))
	testutil.FailErr(t, "find with a fresh catalog", err)
	if first := parseFindResponse(t, out); first.Inventory != nil || len(first.Results) != 1 {
		t.Fatalf("fresh find = %+v", first)
	}

	release := holdMetadataLane(t, root)
	defer release()
	repochange.Notify(t.Context(), repochange.Event{
		ProjectDir: root, Kind: repochange.WorktreeChanged, Source: repochange.SourceMutation,
	})
	out, err = tool.Run(context.Background(), args, nativefixture.Context(root))
	testutil.FailErr(t, "find while the rebuild cannot be admitted", err)
	stale := parseFindResponse(t, out)
	if stale.Inventory == nil || stale.Inventory.Fresh || len(stale.Results) != 1 {
		t.Fatalf("stale find = %+v, want the complete generation reported not fresh", stale)
	}
}
