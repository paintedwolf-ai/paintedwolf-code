package gitcmd

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

// launchRecorder keeps the file authority each confined launch was bound with.
type launchRecorder struct {
	mu            sync.Mutex
	reads, writes []string
}

type launchFunc[S any] func(context.Context, tools.ToolContext, string, string, S) (lycexec.LaunchPlan, func(context.Context, tools.AgentLaunchOutcome), error)

// record wraps next, whatever host sockets or services its launch takes.
func record[S any](r *launchRecorder, next launchFunc[S]) func(context.Context, tools.ToolContext, string, string, S) (lycexec.LaunchPlan, func(context.Context, tools.AgentLaunchOutcome), error) {
	return func(ctx context.Context, tc tools.ToolContext, subject, line string, host S) (lycexec.LaunchPlan, func(context.Context, tools.AgentLaunchOutcome), error) {
		r.mu.Lock()
		r.reads = append(r.reads, tc.Files.SessionReadPaths...)
		r.writes = append(r.writes, tc.Files.GrantedWriteRoots...)
		r.mu.Unlock()
		return next(ctx, tc, subject, line, host)
	}
}

func (r *launchRecorder) reset() {
	r.mu.Lock()
	r.reads, r.writes = nil, nil
	r.mu.Unlock()
}

func (h *harness) bareRemote(t *testing.T, name string) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "eval remote parent", err)
	dir := filepath.Join(parent, name)
	if out, stderr, status := h.direct(parent, "init", "--bare", "-q", "-b", "main", dir); status != 0 {
		t.Fatalf("init bare: %d %s %s", status, out, stderr)
	}
	return dir
}

// A local remote's launch carries every directory Git opens through it: the
// path a file://localhost URL names and an alternate store outside it, which
// Git only reads.
func TestLocalRemotesAdmitEveryDirectoryGitOpens(t *testing.T) {
	h := newHarness(t)
	h.git("reset", "--hard", "HEAD")
	h.git("clean", "-fd")
	recorder := &launchRecorder{}
	h.owners.Launch = record(recorder, UnconfinedLaunch)

	origin := h.bareRemote(t, "origin.git")
	alternate := h.bareRemote(t, "alternate.git")
	testutil.FailErr(t, "write alternates", os.WriteFile(filepath.Join(origin, "objects", "info", "alternates"), []byte(filepath.Join(alternate, "objects")+"\n"), 0o644))
	h.git("remote", "add", "origin", "file://localhost"+filepath.ToSlash(origin))

	if push := h.exec("git", "push", "origin", "main"); push.err != nil || push.result.Status != 0 {
		t.Fatalf("push: %+v %v %s", push.result, push.err, push.stderr)
	}
	// The object store lies inside the remote path the launch already writes.
	if !slices.Contains(recorder.writes, origin) {
		t.Fatalf("push launch writes %v, want %s", recorder.writes, origin)
	}
	if !slices.Contains(recorder.reads, filepath.Join(alternate, "objects")) || slices.Contains(recorder.writes, filepath.Join(alternate, "objects")) {
		t.Fatalf("push launch reads %v, writes %v; want the alternate read only", recorder.reads, recorder.writes)
	}

	recorder.reset()
	if fetch := h.exec("git", "fetch", "origin"); fetch.err != nil || fetch.result.Status != 0 {
		t.Fatalf("fetch: %+v %v %s", fetch.result, fetch.err, fetch.stderr)
	}
	if !slices.Contains(recorder.reads, origin) || len(recorder.writes) != 0 && slices.Contains(recorder.writes, origin) {
		t.Fatalf("fetch launch reads %v, writes %v", recorder.reads, recorder.writes)
	}
}

// A remote helper runs a local program its URL names; agent Git refuses it.
func TestRemoteHelperURLsRefuse(t *testing.T) {
	h := newHarness(t)
	h.exec("git", "ls-remote", "testhelper::anything").refused(t, "remote helper", "COMMAND_OPERAND_INVALID")
}

// A push the remote rejects may already have updated other refs, so it
// settles with an unknown effect rather than none.
func TestRejectedPushSettlesUnknown(t *testing.T) {
	h := newHarness(t)
	h.git("reset", "--hard", "HEAD")
	h.git("clean", "-fd")
	origin := h.bareRemote(t, "origin.git")
	h.git("remote", "add", "origin", origin)
	h.git("push", "-q", "origin", "main")
	other := filepath.Join(t.TempDir(), "other")
	if out, stderr, status := h.direct(filepath.Dir(other), "clone", "-q", origin, other); status != 0 {
		t.Fatalf("clone: %d %s %s", status, out, stderr)
	}
	testutil.FailErr(t, "advance", os.WriteFile(filepath.Join(other, "ahead.txt"), []byte("ahead\n"), 0o644))
	for _, args := range [][]string{{"add", "ahead.txt"}, {"-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "-m", "ahead"}, {"push", "-q", "origin", "main"}} {
		if out, stderr, status := h.direct(other, args...); status != 0 {
			t.Fatalf("git %v: %d %s %s", args, status, out, stderr)
		}
	}
	h.write("behind.txt", "behind\n")
	h.git("add", "behind.txt")
	h.git("commit", "-q", "-m", "behind")
	push := h.exec("git", "push", "origin", "main")
	if push.result.Status == 0 || push.result.EffectCertainty != toolcontract.EffectUnknown {
		t.Fatalf("rejected push settled %+v, want a failure with unknown effect", push.result)
	}
}

// git init and git worktree add bind their launch with the directory they
// create, so an approved path outside the project is writable.
func TestTargetFormsLaunchWithTheirTarget(t *testing.T) {
	h := newHarness(t)
	recorder := &launchRecorder{}
	h.owners.Launch = record(recorder, UnconfinedLaunch)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "outside", err)

	if r := h.exec("git", "init", "-q", filepath.Join(outside, "fresh")); r.err != nil || r.result.Status != 0 {
		t.Fatalf("init: %+v %v %s", r.result, r.err, r.stderr)
	}
	if !slices.Contains(recorder.writes, outside) {
		t.Fatalf("init launch writes %v, want %s", recorder.writes, outside)
	}
	recorder.reset()
	if r := h.exec("git", "worktree", "add", "-b", "side", filepath.Join(outside, "side")); r.err != nil || r.result.Status != 0 {
		t.Fatalf("worktree add: %+v %v %s", r.result, r.err, r.stderr)
	}
	if !slices.Contains(recorder.writes, outside) {
		t.Fatalf("worktree launch writes %v, want %s", recorder.writes, outside)
	}
}
