package survey

import (
	"context"
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

func TestFindReadsLiveWhileTheCatalogHasNoGeneration(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
	release := holdMetadataLane(t, root)
	defer release()

	tool := &FindTool{Boundary: nativefixture.Boundary(t), Catalog: sourcecatalog.New()}
	out, err := tool.Run(context.Background(), map[string]any{"name_glob": "**/*.go"}, nativefixture.Context(root))
	testutil.FailErr(t, "find before inventory", err)
	if found := parseFindResponse(t, out); len(found.Results) != 1 || found.Results[0].Path != "main.go" {
		t.Fatalf("find before inventory = %+v", found)
	}
	grep := &GrepTool{Boundary: nativefixture.Boundary(t), Catalog: tool.Catalog}
	grepOut, err := grep.Run(t.Context(), map[string]any{"path": ".", "pattern": "package main"}, nativefixture.Context(root))
	testutil.FailErr(t, "grep before inventory", err)
	if matches := nativefixture.GrepMatches(t, grepOut); len(matches) != 1 || matches[0]["path"] != "main.go" {
		t.Fatalf("grep before inventory = %+v", matches)
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
	testutil.FailErr(t, "write changed disk", os.WriteFile(filepath.Join(root, "late.go"), []byte("package late"), 0o644))
	repochange.Notify(t.Context(), repochange.Event{
		ProjectDir: root, Kind: repochange.WorktreeChanged, Source: repochange.SourceMutation,
	})
	out, err = tool.Run(context.Background(), args, nativefixture.Context(root))
	testutil.FailErr(t, "find while the rebuild cannot be admitted", err)
	stale := parseFindResponse(t, out)
	if stale.Inventory == nil || stale.Inventory.Fresh || len(stale.Results) != 2 {
		t.Fatalf("stale find = %+v, want both live files with stale inventory reported", stale)
	}
	grep := &GrepTool{Boundary: nativefixture.Boundary(t), Catalog: catalog}
	grepOut, err := grep.Run(t.Context(), map[string]any{"path": ".", "pattern": "package late"}, nativefixture.Context(root))
	testutil.FailErr(t, "grep changed disk", err)
	if matches := nativefixture.GrepMatches(t, grepOut); len(matches) != 1 || matches[0]["path"] != "late.go" {
		t.Fatalf("grep changed disk = %+v", matches)
	}
	listing := &ListDirTool{Boundary: nativefixture.Boundary(t), Catalog: catalog}
	listed, err := listing.Run(t.Context(), map[string]any{"path": ".", "max_depth": 1}, nativefixture.Context(root))
	testutil.FailErr(t, "list changed disk", err)
	if got := decodeListDirOut(t, listed); len(got.Entries) != 2 {
		t.Fatalf("list changed disk = %+v", got)
	}
}
