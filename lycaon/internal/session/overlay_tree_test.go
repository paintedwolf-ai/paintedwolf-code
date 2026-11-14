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
	if fmt.Sprint(changes) != fmt.Sprint([]workspacebaseline.Change{{RootID: "root", ScopeRel: "gen.txt", Path: "gen.txt", Kind: workspacebaseline.ChangeCreated}}) {
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

func TestOverlayTreeNamesMovesWithUnchangedContent(t *testing.T) {
	data := t.TempDir()
	branch := filepath.Join(data, "branch")
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(branch, "a"), 0o755))
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(branch, "a", "x.go"), []byte("x\n"), 0o644))
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(branch, "a", "y.go"), []byte("y\n"), 0o644))
	blobs := sourceblob.New(filepath.Join(data, "source-content"))
	store := workspacebaseline.New(nil, blobs, filepath.Join(data, "manifests"))
	manifest, err := store.Capture(context.Background(), "job", workspacebaseline.Branch(nil, branch))
	testutil.FailErr(t, "capture", err)
	testutil.FailErr(t, "move", os.Rename(filepath.Join(branch, "a"), filepath.Join(branch, "b")))
	testutil.FailErr(t, "edit", os.WriteFile(filepath.Join(branch, "b", "y.go"), []byte("z\n"), 0o644))
	same := workspacebaseline.PathMove{From: "a/x.go", To: "b/x.go"}
	edited := workspacebaseline.PathMove{From: "a/y.go", To: "b/y.go"}
	tree := tools.SourceTree{BaselinePath: manifest, BranchRoot: branch, Roots: []projectroot.RootRef{{ID: "root", Path: "/project", IsPrimary: true}}}
	unchanged, err := OverlayTree{}.TreeMovesUnchanged(t.Context(), tree, []workspacebaseline.PathMove{same, edited})
	testutil.FailErr(t, "moves", err)
	if len(unchanged) != 1 || !unchanged[same] {
		t.Fatalf("unchanged = %+v, want only the file that kept its bytes", unchanged)
	}
	if _, err := (OverlayTree{}).TreeMovesUnchanged(t.Context(), tools.SourceTree{BranchRoot: branch}, []workspacebaseline.PathMove{same}); err == nil {
		t.Fatal("moves with no baseline succeeded")
	}
}

func TestOverlayTreeReportsAMissingBranch(t *testing.T) {
	data := t.TempDir()
	branch := filepath.Join(data, "branch")
	testutil.FailErr(t, "mkdir", os.MkdirAll(branch, 0o755))
	store := workspacebaseline.New(nil, sourceblob.New(filepath.Join(data, "source-content")), filepath.Join(data, "manifests"))
	manifest, err := store.Capture(t.Context(), "job", workspacebaseline.Branch(nil, branch))
	testutil.FailErr(t, "capture", err)
	tree := tools.SourceTree{BaselinePath: manifest, BranchRoot: filepath.Join(data, "reclaimed")}
	if _, err := (OverlayTree{}).TreeChanges(t.Context(), tree); err == nil {
		t.Fatal("comparing a reclaimed branch succeeded")
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
