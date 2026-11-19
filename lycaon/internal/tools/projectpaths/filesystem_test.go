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

func TestNativeSharedTempReadPreservesExternalScope(t *testing.T) {
	project, scratch := t.TempDir(), t.TempDir()
	path := filepath.Join(scratch, "scratch.txt")
	root := confine.SharedTempLocation(path)
	if root == "" {
		t.Fatal("fixture outside shared temp")
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
	_, err = projectpaths.ResolveWrite(t.Context(), nil, tc, path)
	requireReject(t, err, "SURVEY_PATH_ESCAPE")
	if read.Root.Path != root || !read.External || len(recorder.paths) != 0 {
		t.Fatalf("scratch became project state or changed authority: read=%+v recorded=%v", read, recorder.paths)
	}
	if _, err := projectpaths.ResolveWrite(t.Context(), nil, tc, filepath.Join(scratch, ".git", "config")); err == nil {
		t.Fatal("scratch authority bypassed protected metadata")
	}
	tc.Source.WorkerBranchRoot = project
	if _, err := projectpaths.ResolveRead(t.Context(), nil, tc, path); err == nil {
		t.Fatal("scratch authority escaped worker isolation")
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

func TestPrivateTempOperandsKeepSessionIdentity(t *testing.T) {
	tc, scratch, rec := scratchToolContext(t)
	confine.TestingSetAutoConfine(t)
	if len(confine.PrivateTempFor(scratch).Paths) == 0 || !confine.Enforcing() {
		return
	}
	for _, alias := range confine.PrivateTempFor(scratch).Paths {
		path := filepath.Join(alias, "nested", "output.txt")
		want := filepath.Join(scratch, "nested", "output.txt")
		write, err := projectpaths.ResolveWrite(t.Context(), nil, tc, path)
		testutil.FailErr(t, "resolve private temp write", err)
		if write.Abs != want || write.DisplayPath != path || !write.External {
			t.Fatalf("private temp write = %+v", write)
		}
		_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: write.EffectLocation(), Source: strings.NewReader(path), Mode: 0600})
		testutil.FailErr(t, "write to private temp", err)
		frame, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, filepath.Dir(path))
		testutil.FailErr(t, "enter private temp directory", err)
		bound, err := frame.Bind(t.Context(), "output.txt", false)
		testutil.FailErr(t, "bind relative operand in private temp", err)
		if frame.Directory() != filepath.Dir(path) || bound.Resolved.DisplayPath != path || bound.Resolved.Abs != want {
			t.Fatalf("directory = %q, bound file = %+v", frame.Directory(), bound.Resolved)
		}
		session := projectpaths.NewReadSession(nil, tc)
		defer session.Close()
		read, err := session.Resolve(t.Context(), path)
		testutil.FailErr(t, "resolve private temp read", err)
		if read != write {
			t.Fatalf("read = %+v, write = %+v", read, write)
		}
		file, err := session.Open(t.Context(), read)
		testutil.FailErr(t, "open private temp", err)
		data, err := io.ReadAll(file)
		testutil.FailErr(t, "read private temp", err)
		testutil.FailErr(t, "close private temp", file.Close())
		if string(data) != path || len(rec.mutations) > 0 {
			t.Fatalf("read %q, mutations %v", data, rec.mutations)
		}
		approval, err := tools.ApprovalFilePath(tc, path)
		testutil.FailErr(t, "locate approval subject", err)
		tc.Execution.HostExecution = true
		host, err := tools.ApprovalFilePath(tc, path)
		testutil.FailErr(t, "locate host-execution subject", err)
		tc.Execution.HostExecution = false
		if host != path || approval != want || tools.SocketArgPath(tc, path) != path {
			t.Fatal("file and socket subjects lost their namespaces")
		}
	}
}
