package wiring

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

// Agent Git runs through the real command program under the agent's own
// confined launch: local reads, a commit, and a push to and fetch from a local
// remote all complete, and the remote receives exactly what was pushed.
func TestCommandProgramGitRunsUnderTheAgentLaunch(t *testing.T) {
	gitengine.TestingEnableBundledBinary()
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX", "on")
	s := newCommandProgramSession(t, strings.Join([]string{
		"git status --short",
		"git add notes.txt",
		"git commit -q -m agent",
		"git push -q remote.git HEAD:refs/heads/main",
		"git fetch -q remote.git main",
		"git rev-parse FETCH_HEAD",
	}, " && "), "")
	gittest.InitCommit(t, s.dir, "seed")
	gittest.Run(t, s.dir, "init", "-q", "--bare", "remote.git")
	testutil.FailErr(t, "change notes", os.WriteFile(filepath.Join(s.dir, "notes.txt"), []byte("agent note\n"), 0o644))

	testutil.FailErr(t, "prompt", s.prompt(t.Context()))
	result := s.result(t)
	head := strings.TrimSpace(gittest.Run(t, s.dir, "rev-parse", "HEAD"))
	pushed := strings.TrimSpace(gittest.Run(t, filepath.Join(s.dir, "remote.git"), "rev-parse", "refs/heads/main"))
	if pushed != head || !strings.Contains(result, head) {
		t.Fatalf("remote main %q, local HEAD %q, result %q", pushed, head, result)
	}
	if state := s.programState(t); state != "completed" {
		t.Fatalf("program state = %q, result %q", state, result)
	}
	// One completed git child receipt per statement shows each ran as a child
	// of the agent's command program.
	executions := s.executions(t)
	if len(executions) != 6 {
		t.Fatalf("child receipts = %v, want one per git statement", executions)
	}
	for _, row := range executions {
		if !strings.HasPrefix(row, "git") || !strings.HasSuffix(row, "=completed") {
			t.Fatalf("child receipts = %v, want completed git children only", executions)
		}
	}
	if outcome := s.outcome(t, "program-1"); outcome.NativeGaps != 0 {
		t.Fatalf("git ran as an installed-program gap %d times", outcome.NativeGaps)
	}
}
