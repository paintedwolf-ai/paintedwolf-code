package workspace_test

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
)

func TestJobMetaRejectsUnknownFields(t *testing.T) {
	metaDir := t.TempDir()
	raw := `{
  "roots": [{"id":"root","path":"/project","is_primary":true}],
  "snapshot_complete": false,
  "unexpected": true
}`
	testutil.FailErr(t, "write metadata", os.WriteFile(filepath.Join(metaDir, "state.json"), []byte(raw), 0o600))
	if _, err := workspace.LoadJobMeta(metaDir); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("LoadJobMeta error = %v want unknown field", err)
	}
}

func TestLoadBranchLayoutRequiresAbsoluteRoot(t *testing.T) {
	if _, err := workspace.LoadBranchLayout("relative"); err == nil {
		t.Fatal("LoadBranchLayout accepted a relative root")
	}
}

func TestLoadBranchLayoutRejectsSymlinkRoot(t *testing.T) {
	target := t.TempDir()
	branch := filepath.Join(t.TempDir(), "branch")
	if err := os.Symlink(target, branch); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := workspace.LoadBranchLayout(branch); err == nil {
		t.Fatal("LoadBranchLayout accepted a symlink root")
	}
}

func TestJobMetaDerivesTopologyFromRoots(t *testing.T) {
	metaDir := t.TempDir()
	meta := workspace.JobMeta{
		Roots: []workspace.JobMetaRoot{
			{ID: "app", Path: "/project/app", Label: "app", IsPrimary: true},
			{ID: "docs", Path: "/project/docs", Label: "docs"},
		},
	}
	testutil.FailErr(t, "write metadata", workspace.WriteJobMeta(metaDir, meta))
	loaded, err := workspace.LoadJobMeta(metaDir)
	testutil.FailErr(t, "load metadata", err)
	if len(loaded.Roots) != 2 || loaded.Roots[0].ID != "app" || loaded.Roots[1].ID != "docs" {
		t.Fatalf("roots = %+v", loaded.Roots)
	}
}

func TestJobMetaRejectsNonCanonicalLabels(t *testing.T) {
	tests := []struct {
		name  string
		roots []workspace.JobMetaRoot
	}{
		{
			name: "padded",
			roots: []workspace.JobMetaRoot{
				{ID: "app", Path: "/project/app", Label: " app ", IsPrimary: true},
				{ID: "docs", Path: "/project/docs", Label: "docs"},
			},
		},
		{
			name: "case collision",
			roots: []workspace.JobMetaRoot{
				{ID: "app", Path: "/project/app", Label: "Code", IsPrimary: true},
				{ID: "docs", Path: "/project/docs", Label: "code"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := workspace.WriteJobMeta(t.TempDir(), workspace.JobMeta{Roots: tc.roots}); err == nil {
				t.Fatal("expected invalid label error")
			}
		})
	}
}

func TestLoadBranchLayoutValidatesEveryMultiRootDirectory(t *testing.T) {
	branchRoot := filepath.Join(t.TempDir(), "branch")
	testutil.FailErr(t, "create branch root", os.MkdirAll(branchRoot, 0o750))
	meta := workspace.JobMeta{
		Roots: []workspace.JobMetaRoot{
			{ID: "app", Path: "/project/app", Label: "app", IsPrimary: true},
			{ID: "docs", Path: "/project/docs", Label: "docs"},
		},
		SnapshotComplete: true,
	}
	testutil.FailErr(t, "write branch metadata", workspace.WriteJobMeta(enginepaths.MetaDirForBranchRoot(branchRoot), meta))
	rootDirs := make(map[string]string)
	for _, id := range []string{"app", "docs"} {
		dir, err := projectroot.BranchDirForID(id)
		testutil.FailErr(t, "derive child root", err)
		rootDirs[id] = dir
		testutil.FailErr(t, "create child root", os.MkdirAll(filepath.Join(branchRoot, dir), 0o750))
	}

	if _, err := workspace.LoadBranchLayout(branchRoot); err != nil {
		t.Fatalf("LoadBranchLayout complete topology: %v", err)
	}
	testutil.FailErr(t, "remove child root", os.Remove(filepath.Join(branchRoot, rootDirs["docs"])))
	if _, err := workspace.LoadBranchLayout(branchRoot); err == nil || !strings.Contains(err.Error(), `root "docs" is unavailable`) {
		t.Fatalf("LoadBranchLayout missing child error = %v", err)
	}
}

func TestLoadBranchLayoutRejectsMultiRootSymlink(t *testing.T) {
	branchRoot := filepath.Join(t.TempDir(), "branch")
	appDir, err := projectroot.BranchDirForID("app")
	testutil.FailErr(t, "derive app root", err)
	docsDir, err := projectroot.BranchDirForID("docs")
	testutil.FailErr(t, "derive docs root", err)
	testutil.FailErr(t, "create branch root", os.MkdirAll(filepath.Join(branchRoot, appDir), 0o750))
	if err := os.Symlink(t.TempDir(), filepath.Join(branchRoot, docsDir)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	meta := workspace.JobMeta{
		Roots: []workspace.JobMetaRoot{
			{ID: "app", Path: "/project/app", Label: "app", IsPrimary: true},
			{ID: "docs", Path: "/project/docs", Label: "docs"},
		},
		SnapshotComplete: true,
	}
	testutil.FailErr(t, "write branch metadata", workspace.WriteJobMeta(enginepaths.MetaDirForBranchRoot(branchRoot), meta))
	if _, err := workspace.LoadBranchLayout(branchRoot); err == nil || !strings.Contains(err.Error(), `root "docs" is not a directory`) {
		t.Fatalf("LoadBranchLayout symlink child error = %v", err)
	}
}

func TestJobMetaRejectsUnrecognizedFormat(t *testing.T) {
	dir := t.TempDir()
	for _, version := range []int{0, 2} {
		raw := fmt.Sprintf(`{"format_version":%d,"roots":[{"id":"app","path":"/project","is_primary":true}],"snapshot_complete":false}`, version)
		testutil.FailErr(t, "write unsupported metadata", os.WriteFile(filepath.Join(dir, "state.json"), []byte(raw), 0o600))
		if _, err := workspace.LoadJobMeta(dir); err == nil {
			t.Fatalf("accepted metadata format %d", version)
		}
	}
}
