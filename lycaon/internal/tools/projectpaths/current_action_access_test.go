package projectpaths_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type mutationRecorder struct{ paths []string }

func (r *mutationRecorder) RecordPrimaryMutation(_ context.Context, _, path string) {
	r.paths = append(r.paths, path)
}

func TestApprovedExternalWriteDoesNotRecordAProjectRewindPath(t *testing.T) {
	project := t.TempDir()
	outside := filepath.Join(filepath.VolumeName(project)+string(filepath.Separator), "unattached", t.Name())
	target := filepath.Join(outside, "notes.txt")
	recorder := &mutationRecorder{}
	tc := tools.ToolContext{
		Roots: []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}}, ActiveRootID: "root",
		MutationRecorder:   recorder,
		ApprovedFileAccess: []hitl.GrantedPathDelta{{Path: grantedpath.Normalize(target), Write: true}},
	}
	_, err := projectpaths.ResolveWrite(t.Context(), nil, tc, target)
	testutil.FailErr(t, "resolve external write", err)
	if len(recorder.paths) != 0 {
		t.Fatalf("external write recorded primary paths: %v", recorder.paths)
	}
	_, err = projectpaths.ResolveWrite(t.Context(), nil, tc, "notes.txt")
	testutil.FailErr(t, "resolve project write", err)
	if len(recorder.paths) != 1 || recorder.paths[0] != "notes.txt" {
		t.Fatalf("project write not recorded: %v", recorder.paths)
	}
}

func TestCurrentActionFileAccessIsExactAndDirectional(t *testing.T) {
	project := t.TempDir()
	outside := filepath.Join(filepath.VolumeName(project)+string(filepath.Separator), "unattached", t.Name())
	target := filepath.Join(outside, "notes.txt")
	tc := tools.ToolContext{
		Roots: []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}}, ActiveRootID: "root",
		ApprovedFileAccess: []hitl.GrantedPathDelta{{Path: grantedpath.Normalize(target)}},
	}
	_, err := projectpaths.ResolveRead(t.Context(), nil, tc, target)
	testutil.FailErr(t, "read approved path", err)
	if _, err := projectpaths.ResolveWrite(t.Context(), nil, tc, target); err == nil {
		t.Fatal("read approval authorized a write")
	}
	for _, path := range []string{outside, filepath.Join(target, "child"), filepath.Join(outside, "sibling")} {
		if _, err := projectpaths.ResolveRead(t.Context(), nil, tc, path); err == nil {
			t.Fatalf("exact approval authorized %q", path)
		}
	}
}

func TestCurrentActionFileAccessRejectsRetargetedSymlink(t *testing.T) {
	project, outside := t.TempDir(), t.TempDir()
	alias := filepath.Join(outside, "alias")
	first := filepath.Join(filepath.VolumeName(project)+string(filepath.Separator), "unattached", t.Name(), "first")
	second := filepath.Join(filepath.Dir(first), "second")
	testutil.FailErr(t, "create reviewed alias", os.Symlink(first, alias))
	target := filepath.Join(alias, "notes.txt")
	tc := tools.ToolContext{
		Roots: []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}}, ActiveRootID: "root",
		ApprovedFileAccess: []hitl.GrantedPathDelta{{Path: grantedpath.Normalize(target), Write: true}},
	}
	testutil.FailErr(t, "remove reviewed alias", os.Remove(alias))
	testutil.FailErr(t, "retarget alias", os.Symlink(second, alias))
	if _, err := projectpaths.ResolveWrite(t.Context(), nil, tc, target); err == nil {
		t.Fatal("approval followed a changed symlink")
	}
}
