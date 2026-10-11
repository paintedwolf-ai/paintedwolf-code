package projectpaths_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type mockMutationRecorder struct {
	mutations []string
}

func (m *mockMutationRecorder) RecordPrimaryMutation(_ context.Context, _ string, relPath string) {
	m.mutations = append(m.mutations, relPath)
}

func scratchToolContext(t *testing.T) (tools.ToolContext, string, *mockMutationRecorder) {
	t.Helper()
	ws := t.TempDir()
	scratchDir := fspath.CanonicalPath(t.TempDir())
	rec := &mockMutationRecorder{}
	return tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Path: ws, IsPrimary: true}},
			ActiveRootID:     "r1",
			MutationRecorder: rec},
		Host:     tools.InvocationHost{SessionScratchDir: scratchDir},
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID},
	}, scratchDir, rec
}

func requireReject(t *testing.T, err error, code string) {
	t.Helper()
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

func TestResolveSessionScratchAddresses(t *testing.T) {
	tctx, scratchDir, rec := scratchToolContext(t)
	ctx := context.Background()

	cases := []struct {
		path        string
		wantAbs     string
		wantDisplay string
	}{
		{path: "@scratch", wantAbs: scratchDir, wantDisplay: "@scratch"},
		{path: "@scratch/out.txt", wantAbs: filepath.Join(scratchDir, "out.txt"), wantDisplay: "@scratch/out.txt"},
		{path: "@Scratch/nested/sub.txt", wantAbs: filepath.Join(scratchDir, "nested", "sub.txt"), wantDisplay: "@scratch/nested/sub.txt"},
		{path: "@scratch/a/../b.txt", wantAbs: filepath.Join(scratchDir, "b.txt"), wantDisplay: "@scratch/b.txt"},
		{path: filepath.Join(scratchDir, "abs.txt"), wantAbs: filepath.Join(scratchDir, "abs.txt"), wantDisplay: "@scratch/abs.txt"},
	}
	for _, tc := range cases {
		res, err := projectpaths.ResolveRead(ctx, nil, tctx, tc.path)
		testutil.FailErr(t, "ResolveRead "+tc.path, err)
		if res.Abs != tc.wantAbs || res.DisplayPath != tc.wantDisplay || !res.External {
			t.Fatalf("ResolveRead(%q) = abs %q display %q external %v; want %q %q true",
				tc.path, res.Abs, res.DisplayPath, res.External, tc.wantAbs, tc.wantDisplay)
		}
	}

	res, err := projectpaths.ResolveWrite(ctx, nil, tctx, "@scratch/out.txt")
	testutil.FailErr(t, "ResolveWrite @scratch/out.txt", err)
	if !res.External || res.Root.Path != scratchDir {
		t.Fatalf("scratch write resolved as %+v", res)
	}
	if len(rec.mutations) != 0 {
		t.Fatalf("scratch writes recorded project mutations: %v", rec.mutations)
	}

	// An ordinary project path keeps its project identity, whatever its name.
	res, err = projectpaths.ResolveWrite(ctx, nil, tctx, ".tmp/file.txt")
	testutil.FailErr(t, "ResolveWrite .tmp/file.txt", err)
	if res.External || len(rec.mutations) != 1 || rec.mutations[0] != ".tmp/file.txt" {
		t.Fatalf("in-project write = %+v, mutations %v", res, rec.mutations)
	}
}

func TestResolveSessionScratchRefusesEscapes(t *testing.T) {
	tctx, scratchDir, _ := scratchToolContext(t)
	ctx := context.Background()

	for _, path := range []string{"@scratch/..", "@scratch/../../escape.txt", "@scratch//etc/passwd"} {
		_, err := projectpaths.ResolveRead(ctx, nil, tctx, path)
		requireReject(t, err, "SURVEY_PATH_ESCAPE")
	}

	outside := t.TempDir()
	testutil.FailErr(t, "plant link", os.Symlink(outside, filepath.Join(scratchDir, "escapelink")))
	_, err := projectpaths.ResolveRead(ctx, nil, tctx, "@scratch/escapelink/secret.txt")
	requireReject(t, err, "SURVEY_PATH_ESCAPE")
	_, err = projectpaths.ResolveWrite(ctx, nil, tctx, "@scratch/escapelink/secret.txt")
	requireReject(t, err, "SURVEY_PATH_ESCAPE")

	// A link that stays inside the folder is an ordinary scratch path.
	testutil.FailErr(t, "mkdir inner", os.MkdirAll(filepath.Join(scratchDir, "inner"), 0o700))
	testutil.FailErr(t, "plant inner link", os.Symlink(filepath.Join(scratchDir, "inner"), filepath.Join(scratchDir, "alias")))
	_, err = projectpaths.ResolveRead(ctx, nil, tctx, "@scratch/alias/notes.txt")
	testutil.FailErr(t, "ResolveRead through an inner link", err)
}

func TestResolveSessionScratchUnavailable(t *testing.T) {
	tctx, _, _ := scratchToolContext(t)
	tctx.Host.SessionScratchDir = ""
	_, err := projectpaths.ResolveRead(context.Background(), nil, tctx, "@scratch/file.txt")
	requireReject(t, err, tools.SessionScratchUnavailableCode)
}

// A read-only worker has no branch to write project files into, but its own
// scratch is not project state.
func TestReadOnlyWorkerWritesItsOwnScratch(t *testing.T) {
	tctx, scratchDir, rec := scratchToolContext(t)
	tctx.Identity.WorkerJobID = "worker-1"
	ctx := context.Background()

	res, err := projectpaths.ResolveWrite(ctx, nil, tctx, "@scratch/findings.md")
	testutil.FailErr(t, "worker ResolveWrite @scratch/findings.md", err)
	if res.Abs != filepath.Join(scratchDir, "findings.md") {
		t.Fatalf("worker scratch write abs = %q", res.Abs)
	}
	if len(rec.mutations) != 0 {
		t.Fatalf("worker scratch write recorded project mutations: %v", rec.mutations)
	}

	_, err = projectpaths.ResolveWrite(ctx, nil, tctx, "notes.md")
	requireReject(t, err, "WORKER_WRITE_WITHOUT_BRANCH")
}

func TestWorkerScratchIsSiblingNotShared(t *testing.T) {
	scratchRoot := fspath.CanonicalPath(t.TempDir())
	coordinatorDir := filepath.Join(scratchRoot, "chat-1")
	worker1Dir := filepath.Join(scratchRoot, "worker-1")
	worker2Dir := filepath.Join(scratchRoot, "worker-2")
	for _, dir := range []string{coordinatorDir, worker1Dir, worker2Dir} {
		testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(dir, 0o700))
	}
	worker1, _, _ := scratchToolContext(t)
	worker1.Host.SessionScratchDir = worker1Dir
	worker1.Identity.WorkerJobID = "job-1"
	worker1.Source.WorkerBranchRoot = t.TempDir()
	ctx := context.Background()

	_, err := projectpaths.ResolveRead(ctx, nil, worker1, "@scratch/../worker-2/secret.txt")
	requireReject(t, err, "SURVEY_PATH_ESCAPE")
	for _, other := range []string{filepath.Join(worker2Dir, "secret.txt"), filepath.Join(coordinatorDir, "plan.md")} {
		res, err := projectpaths.ResolveRead(ctx, nil, worker1, other)
		if err == nil && res.Root.ID == projectroot.VirtualScratchLabel {
			t.Fatalf("worker resolved %q as its own scratch", other)
		}
	}
}

func TestCommandCwdSessionScratch(t *testing.T) {
	tctx, scratchDir, _ := scratchToolContext(t)
	testutil.FailErr(t, "mkdir nested", os.MkdirAll(filepath.Join(scratchDir, "nested"), 0o700))
	ctx := context.Background()

	cases := []struct {
		cwd         string
		wantAbs     string
		wantDisplay string
	}{
		{cwd: "@scratch", wantAbs: scratchDir, wantDisplay: "@scratch"},
		{cwd: "@Scratch", wantAbs: scratchDir, wantDisplay: "@scratch"},
		{cwd: "@scratch/nested", wantAbs: filepath.Join(scratchDir, "nested"), wantDisplay: "@scratch/nested"},
		{cwd: filepath.Join(scratchDir, "nested"), wantAbs: filepath.Join(scratchDir, "nested"), wantDisplay: "@scratch/nested"},
	}
	for _, tc := range cases {
		abs, display, err := projectpaths.CommandCwd(ctx, tctx, tc.cwd)
		testutil.FailErr(t, "CommandCwd "+tc.cwd, err)
		if abs != tc.wantAbs || display != tc.wantDisplay {
			t.Fatalf("CommandCwd(%q) = (%q, %q), want (%q, %q)", tc.cwd, abs, display, tc.wantAbs, tc.wantDisplay)
		}
	}

	_, _, err := projectpaths.CommandCwd(ctx, tctx, "@scratch/nonexistent")
	requireReject(t, err, "CWD_NOT_DIRECTORY")
	_, _, err = projectpaths.CommandCwd(ctx, tctx, "@scratch/../../escape")
	requireReject(t, err, "CWD_OUT_OF_SCOPE")

	verify := tctx
	verify.Execution.VerificationCheck = true
	_, _, err = projectpaths.CommandCwd(ctx, verify, "@scratch")
	requireReject(t, err, "CWD_SCRATCH_NOT_VERIFICATION")

	unavailable := tctx
	unavailable.Host.SessionScratchDir = ""
	_, _, err = projectpaths.CommandCwd(ctx, unavailable, "@scratch")
	requireReject(t, err, tools.SessionScratchUnavailableCode)

	worker := tctx
	worker.Identity.WorkerJobID = "w1"
	worker.Source.WorkerBranchRoot = t.TempDir()
	abs, _, err := projectpaths.CommandCwd(ctx, worker, "@scratch")
	testutil.FailErr(t, "worker CommandCwd @scratch", err)
	if abs != scratchDir {
		t.Fatalf("worker scratch cwd = %q, want %q", abs, scratchDir)
	}
}
