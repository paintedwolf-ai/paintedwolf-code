package projectpaths_test

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestNativeScratchUsesProcessFilesystemRoots(t *testing.T) {
	project, scratch := t.TempDir(), t.TempDir()
	path := filepath.Join(scratch, "scratch.txt")
	root, err := confine.FilesystemRootForPath("project", []string{project}, nil, "", path)
	testutil.FailErr(t, "resolve process filesystem root", err)
	if root == "" {
		t.Fatal("OS temporary directory missing from process boundary")
	}
	recorder := &mutationRecorder{}
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: "project"},
		Source: tools.InvocationSource{MutationRecorder: recorder,
			Roots:        []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}},
			ActiveRootID: "root"},
	}
	read, err := projectpaths.ResolveRead(t.Context(), nil, tc, path)
	testutil.FailErr(t, "resolve native scratch read", err)
	write, err := projectpaths.ResolveWrite(t.Context(), nil, tc, path)
	testutil.FailErr(t, "resolve native scratch write", err)
	if read.Root.Path != root || write.Root.Path != root || !read.External || !write.External || len(recorder.paths) != 0 {
		t.Fatalf("scratch became project state or changed authority: read=%+v write=%+v recorded=%v", read, write, recorder.paths)
	}
	if _, err := projectpaths.ResolveWrite(t.Context(), nil, tc, filepath.Join(scratch, ".git", "config")); err == nil {
		t.Fatal("scratch authority bypassed protected metadata")
	}
	tc.Source.WorkerBranchRoot = project
	if _, err := projectpaths.ResolveRead(t.Context(), nil, tc, path); err == nil {
		t.Fatal("scratch authority escaped worker isolation")
	}
}

func TestNativeWriteCreatesAnAbsentCacheRootThroughTheDescriptorDoor(t *testing.T) {
	project := t.TempDir()
	cache := filepath.Join(t.TempDir(), "new-cache")
	t.Setenv("XDG_CACHE_HOME", cache)
	path := filepath.Join(cache, "package", "manifest.txt")
	tc := tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}},
			ActiveRootID: "root"},
	}
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, nil)
	resolved, err := projectpaths.ResolveWrite(t.Context(), boundary, tc, path)
	testutil.FailErr(t, "resolve absent cache file", err)
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: resolved.EffectLocation(), Source: strings.NewReader("ready"), Mode: 0o600})
	testutil.FailErr(t, "create cache file", err)
	file, err := fseffect.OpenRead(resolved.EffectLocation())
	testutil.FailErr(t, "open cache file", err)
	defer file.Close()
	data, err := io.ReadAll(file)
	testutil.FailErr(t, "read cache file", err)
	if string(data) != "ready" || !resolved.External {
		t.Fatalf("cache write changed data or project identity: %q, %+v", data, resolved)
	}
}
