package projectpaths_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestNativeSharedTempReadPreservesExternalScope(t *testing.T) {
	project, sharedDir := t.TempDir(), t.TempDir()
	path := filepath.Join(sharedDir, "incoming.txt")
	sibling := filepath.Join(sharedDir, "sibling.txt")
	for _, f := range []string{path, sibling} {
		testutil.FailErr(t, "write fixture", os.WriteFile(f, []byte("host-bytes"), 0o600))
	}
	if confine.SharedTempLocation(path) == "" {
		t.Fatal("fixture outside shared temp")
	}
	recorder := &mutationRecorder{}
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: "project"},
		Host:     tools.InvocationHost{SessionScratchDir: t.TempDir()},
		Source: tools.InvocationSource{MutationRecorder: recorder,
			Roots:        []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}},
			ActiveRootID: "root"},
	}
	_, err := projectpaths.ResolveRead(t.Context(), nil, tc, path)
	requireReject(t, err, "SURVEY_PATH_ESCAPE")

	// Write-only shared-temp authority never admits reads.
	tc.Files.GrantedWriteRoots = []string{sharedDir}
	_, err = projectpaths.ResolveRead(t.Context(), nil, tc, path)
	requireReject(t, err, "SURVEY_PATH_ESCAPE")
	tc.Files.GrantedWriteRoots = nil

	// Exact-file read authority covers only the named file and keeps external scope.
	tc.Files.SessionReadPaths = []string{path}
	read, err := projectpaths.ResolveRead(t.Context(), nil, tc, path)
	testutil.FailErr(t, "resolve approved exact temp read", err)
	if read.Root.Path != fspath.CanonicalPath(sharedDir) || read.Abs != fspath.CanonicalPath(path) || !read.External || len(recorder.paths) != 0 {
		t.Fatalf("exact read became project state or changed authority: read=%+v recorded=%v", read, recorder.paths)
	}
	_, err = projectpaths.ResolveRead(t.Context(), nil, tc, sibling)
	requireReject(t, err, "SURVEY_PATH_ESCAPE")
	_, err = projectpaths.ResolveWrite(t.Context(), nil, tc, path)
	requireReject(t, err, "SURVEY_PATH_ESCAPE")

	// Directory read authority covers descendants while keeping writes closed.
	tc.Files.SessionReadPaths = []string{sharedDir}
	dirRead, err := projectpaths.ResolveRead(t.Context(), nil, tc, sibling)
	testutil.FailErr(t, "resolve approved temp dir read", err)
	if dirRead.Root.Path != fspath.CanonicalPath(sharedDir) || !dirRead.External {
		t.Fatalf("directory read changed anchor: %+v", dirRead)
	}
	if _, err := projectpaths.ResolveWrite(t.Context(), nil, tc, filepath.Join(sharedDir, ".git", "config")); err == nil {
		t.Fatal("shared-temp read authority bypassed protected metadata")
	}
	tc.Source.WorkerBranchRoot = project
	if _, err := projectpaths.ResolveRead(t.Context(), nil, tc, path); err == nil {
		t.Fatal("shared-temp authority escaped worker isolation")
	}
}

func TestReviewedNativeWriteCreatesAnAbsentCacheRoot(t *testing.T) {
	project := t.TempDir()
	cache := filepath.Join(t.TempDir(), "new-cache")
	t.Setenv("XDG_CACHE_HOME", cache)
	path := filepath.Join(cache, "package", "manifest.txt")
	tc := tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}},
			ActiveRootID: "root"},
	}
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, nil)
	_, err := projectpaths.ResolveWrite(t.Context(), boundary, tc, path)
	requireReject(t, err, "SURVEY_PATH_ESCAPE")
	tc.Files.GrantedWriteRoots = []string{cache}
	resolved, err := projectpaths.ResolveWrite(t.Context(), boundary, tc, path)
	testutil.FailErr(t, "resolve absent cache file", err)
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: resolved.EffectLocation(), Source: strings.NewReader("ready"), Mode: 0o600})
	testutil.FailErr(t, "create cache file", err)
	file, err := fseffect.OpenRead(resolved.EffectLocation())
	testutil.FailErr(t, "open cache file", err)
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	testutil.FailErr(t, "read cache file", err)
	if string(data) != "ready" || !resolved.External {
		t.Fatalf("cache write changed data or project identity: %q, %+v", data, resolved)
	}
}
