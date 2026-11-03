package workspace_test

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
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
	for _, version := range []int{-1, 0, 3, 99} {
		raw := fmt.Sprintf(`{"format_version":%d,"roots":[{"id":"app","path":"/project","is_primary":true}],"snapshot_complete":false}`, version)
		path := filepath.Join(dir, "state.json")
		testutil.FailErr(t, "write unsupported metadata", os.WriteFile(path, []byte(raw), 0o600))
		if _, err := workspace.LoadJobMeta(dir); err == nil {
			t.Fatalf("accepted metadata format %d", version)
		}
		if _, _, err := workspace.UpgradeJobMetaToV2(dir); err == nil {
			t.Fatalf("UpgradeJobMetaToV2 accepted metadata format %d", version)
		}
		after, err := os.ReadFile(path)
		testutil.FailErr(t, "read after failed upgrade", err)
		if string(after) != raw {
			t.Fatalf("failed upgrade mutated state.json: got %q want %q", after, raw)
		}
	}
}

func TestJobMetaFormatV1BackwardCompatibilityAndV2Persistence(t *testing.T) {
	v1Dir := t.TempDir()
	v1Raw := `{"format_version":1,"roots":[{"id":"app","path":"/project/app","label":"app","is_primary":true}],"snapshot_complete":true}`
	testutil.FailErr(t, "write v1 metadata", os.WriteFile(filepath.Join(v1Dir, "state.json"), []byte(v1Raw), 0o600))

	loadedV1, err := workspace.LoadJobMeta(v1Dir)
	testutil.FailErr(t, "LoadJobMeta v1", err)
	if loadedV1.FormatVersion != workspace.JobMetaFormatV1 ||
		loadedV1.AdapterID != adapter.AdapterEagerCopy ||
		loadedV1.PreparationKind != adapter.PreparationMaterialized ||
		loadedV1.VisibilityMode != adapter.VisibilityFixed {
		t.Fatalf("expected v1 metadata to load as retained materialized eagercopy with fixed visibility, got %+v", loadedV1)
	}

	// Format-v1 cannot be relabeled as mounted or live.
	badV1 := v1Dir + "-bad"
	testutil.FailErr(t, "mkdir badV1", os.MkdirAll(badV1, 0o700))
	testutil.FailErr(t, "write bad v1", os.WriteFile(filepath.Join(badV1, "state.json"), []byte(
		`{"format_version":1,"roots":[{"id":"app","path":"/project/app","is_primary":true}],"snapshot_complete":true,"preparation_kind":"mounted"}`,
	), 0o600))
	if _, err := workspace.LoadJobMeta(badV1); err == nil {
		t.Fatal("expected Format-v1 metadata with mounted preparation_kind to be rejected")
	}

	upgraded, didUpgrade, err := workspace.UpgradeJobMetaToV2(v1Dir)
	testutil.FailErr(t, "UpgradeJobMetaToV2", err)
	if !didUpgrade || upgraded.FormatVersion != workspace.JobMetaFormatV2 ||
		upgraded.AdapterID != adapter.AdapterEagerCopy ||
		upgraded.PreparationKind != adapter.PreparationMaterialized ||
		upgraded.VisibilityMode != adapter.VisibilityFixed {
		t.Fatalf("expected upgraded v2 metadata to retain materialized fixed semantics, got didUpgrade=%v meta=%+v", didUpgrade, upgraded)
	}

	v2Dir := t.TempDir()
	v2Meta := workspace.JobMeta{
		FormatVersion:    workspace.JobMetaFormatV2,
		Roots:            []workspace.JobMetaRoot{{ID: "app", Path: "/project/app", Label: "app", IsPrimary: true}},
		SnapshotComplete: true,
		JobMetaPreparation: workspace.JobMetaPreparation{
			AdapterID:       adapter.AdapterNFSDarwin,
			PreparationKind: adapter.PreparationMounted,
			VisibilityMode:  adapter.VisibilityLive,
			FallbackReason:  adapter.ReasonNone,
			CopiedEntries:   0,
			CopiedBytes:     0,
			PendingGitInit:  true,
		},
		JobMetaLineage: workspace.JobMetaLineage{
			ViewID:                    "view-v2",
			SourceGeneration:          7,
			JournalPath:               "/runtime/view-v2/journal",
			ParentCheckpointIDs:       []string{"ckpt-1"},
			LastPublishedCheckpointID: "ckpt-2",
			SelectedRootIDs:           []string{"app"},
			FocusPathHints:            []string{"cmd/main.go"},
		},
	}
	testutil.FailErr(t, "WriteJobMeta v2", workspace.WriteJobMeta(v2Dir, v2Meta))
	gotV2, err := workspace.LoadJobMeta(v2Dir)
	testutil.FailErr(t, "LoadJobMeta v2", err)
	if gotV2.FormatVersion != workspace.JobMetaFormatV2 ||
		gotV2.AdapterID != adapter.AdapterNFSDarwin ||
		gotV2.PreparationKind != adapter.PreparationMounted ||
		gotV2.VisibilityMode != adapter.VisibilityLive ||
		gotV2.ViewID != "view-v2" ||
		gotV2.SourceGeneration != 7 ||
		gotV2.JournalPath != "/runtime/view-v2/journal" ||
		gotV2.LastPublishedCheckpointID != "ckpt-2" ||
		!gotV2.PendingGitInit ||
		len(gotV2.ParentCheckpointIDs) != 1 ||
		len(gotV2.FocusPathHints) != 1 {
		t.Fatalf("unexpected v2 round-trip metadata: %+v", gotV2)
	}
}
