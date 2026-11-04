package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

func TestOverlayTreeReadsThePathsPromotionConsiders(t *testing.T) {
	data := t.TempDir()
	branch := filepath.Join(data, "branch")
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(branch, ".paintedwolf"), 0o755))
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(branch, "main.go"), []byte("old\n"), 0o644))
	blobs := sourceblob.New(filepath.Join(data, "source-content"))
	store := workspacebaseline.New(nil, blobs, filepath.Join(data, "manifests"))
	manifest, err := store.Capture(context.Background(), "job", workspacebaseline.Branch(nil, branch))
	testutil.FailErr(t, "capture", err)

	testutil.FailErr(t, "gen", os.WriteFile(filepath.Join(branch, "gen.txt"), []byte("gen\n"), 0o644))
	testutil.FailErr(t, "metadata", os.WriteFile(filepath.Join(branch, ".paintedwolf", "approvals.yaml"), []byte("rules: []\n"), 0o644))
	tree := tools.SourceTree{BaselinePath: manifest, BranchRoot: branch, Roots: []projectroot.RootRef{{ID: "root", Path: "/project", IsPrimary: true}}}

	changes, err := OverlayTree{}.TreeChanges(t.Context(), tree)
	testutil.FailErr(t, "tree changes", err)
	if fmt.Sprint(changes) != fmt.Sprint([]workspacebaseline.Change{{Path: "gen.txt", Kind: workspacebaseline.ChangeCreated}}) {
		t.Fatalf("changes = %+v, want gen.txt alone", changes)
	}
	compared, err := OverlayTree{}.TreePathChange(t.Context(), tree, "gen.txt")
	testutil.FailErr(t, "path change", err)
	if compared.Kind != workspacebaseline.ChangeCreated || compared.After != "gen\n" {
		t.Fatalf("compared = %+v", compared)
	}
	omitted, err := OverlayTree{}.TreePathChange(t.Context(), tree, ".paintedwolf/approvals.yaml")
	testutil.FailErr(t, "omitted path", err)
	if omitted.Kind != "" {
		t.Fatalf("omitted = %+v, want host metadata to read as unchanged", omitted)
	}
}

func TestOverlayTreeReportsAnUnusableBaseline(t *testing.T) {
	tree := tools.SourceTree{BranchRoot: t.TempDir()}
	if _, err := (OverlayTree{}).TreeChanges(t.Context(), tree); err == nil {
		t.Fatal("tree changes with no baseline succeeded")
	}
	if _, err := (OverlayTree{}).TreePathChange(t.Context(), tree, "main.go"); err == nil {
		t.Fatal("path change with no baseline succeeded")
	}
}
