package toolhost

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestGitDisallowedConfigOverridesRefuseWithoutExternalFallback(t *testing.T) {
	bin, err := gitengine.BinaryPath()
	testutil.FailErr(t, "require pinned Git", err)
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	f := newComposedProgramFixture(t)
	var launches atomic.Int64
	configure := f.Configure
	f.Configure = func(tc *tools.ToolContext) {
		if configure != nil {
			configure(tc)
		}
		tc.Source.RecordViewProcess = func(int) error {
			launches.Add(1)
			return nil
		}
	}

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = f.Dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init")
	runGit("config", "user.name", "Test Author")
	runGit("config", "user.email", "test@example.com")
	filePath := filepath.Join(f.Dir, "a.txt")
	testutil.FailErr(t, "seed a.txt", os.WriteFile(filePath, []byte("hello\n"), 0o644))
	runGit("add", "a.txt")
	runGit("commit", "-m", "initial")

	cfgPath := filepath.Join(f.Dir, ".git", "config")
	beforeCfg, err := os.ReadFile(cfgPath)
	testutil.FailErr(t, "read .git/config", err)

	gapRows := func() int {
		t.Helper()
		var n int
		testutil.FailErr(t, "count gap rows", f.Database.QueryRowContext(
			t.Context(),
			`SELECT COUNT(*) FROM invocation_program_executions WHERE native_gap_json != ''`,
		).Scan(&n))
		return n
	}

	prohibited := []struct {
		name   string
		source string
	}{
		{"single fsmonitor override", `git -c core.fsmonitor=fixture-inert-fsmonitor status`},
		{"single diff.external override", `git -c diff.external=fixture-inert-extdiff diff`},
		{"attached mixed-case fsmonitor override", `git -cCore.FSMonitor=fixture-inert-fsmonitor status`},
		{"pipeline fsmonitor override", `git -c core.fsmonitor=fixture-inert-fsmonitor status | cat`},
		{"nested env fsmonitor override", `env git -c core.fsmonitor=fixture-inert-fsmonitor status`},
		{"nested env fsmonitor with unknown flag", `env git -c core.fsmonitor=fixture-inert-fsmonitor status --unknown-flag`},
		{"direct fsmonitor with unknown flag", `git -c core.fsmonitor=fixture-inert-fsmonitor status --unknown-flag`},
		{"unserved subcommand with fsmonitor override", `git -c core.fsmonitor=fixture-inert-fsmonitor count-objects`},
	}

	for _, tc := range prohibited {
		t.Run(tc.name, func(t *testing.T) {
			launches.Store(0)
			outcome := callComposedProgram(t, f, tc.source)
			if outcome.Refusal == nil || outcome.Refusal.Code != "COMMAND_OPTION_UNSUPPORTED" {
				t.Fatalf("%s: got refusal=%+v status=%d stdout=%q stderr=%q, want COMMAND_OPTION_UNSUPPORTED",
					tc.name, outcome.Refusal, outcome.Status, outcome.Stdout, outcome.Stderr)
			}
			if outcome.NativeGaps != 0 {
				t.Fatalf("%s: NativeGaps=%d, want 0", tc.name, outcome.NativeGaps)
			}
			if got := launches.Load(); got != 0 {
				t.Fatalf("%s: launch spy recorded %d launches, want 0", tc.name, got)
			}
			if got := gapRows(); got != 0 {
				t.Fatalf("%s: SQLite recorded %d native_gap rows, want 0", tc.name, got)
			}
			afterCfg, err := os.ReadFile(cfgPath)
			testutil.FailErr(t, "read .git/config after", err)
			if string(afterCfg) != string(beforeCfg) {
				t.Fatalf("%s: .git/config mutated", tc.name)
			}
			afterFile, err := os.ReadFile(filePath)
			testutil.FailErr(t, "read a.txt after", err)
			if string(afterFile) != "hello\n" {
				t.Fatalf("%s: a.txt mutated to %q", tc.name, afterFile)
			}
		})
	}

	t.Run("authorized git controls succeed", func(t *testing.T) {
		for _, source := range []string{
			`git status`,
			`git -c core.fsmonitor=false status`,
			`printf 'updated\n' > a.txt && git add a.txt && git commit -m "authorized commit"`,
		} {
			launches.Store(0)
			outcome := callComposedProgram(t, f, source)
			if outcome.Refusal != nil || outcome.Status != 0 || outcome.NativeGaps != 0 {
				t.Fatalf("%q: refusal=%+v status=%d gaps=%d stderr=%q",
					source, outcome.Refusal, outcome.Status, outcome.NativeGaps, outcome.Stderr)
			}
			if got := launches.Load(); got == 0 {
				t.Fatalf("%q: launch spy recorded 0 launches, want >0", source)
			}
		}
	})

	t.Run("unrelated unsupported git form still falls back", func(t *testing.T) {
		launches.Store(0)
		outcome := callComposedProgram(t, f, `git count-objects`)
		if outcome.Refusal != nil || outcome.Status != 0 || outcome.NativeGaps != 1 {
			t.Fatalf("git count-objects: refusal=%+v status=%d gaps=%d stderr=%q",
				outcome.Refusal, outcome.Status, outcome.NativeGaps, outcome.Stderr)
		}
		if !strings.Contains(outcome.Stdout, "objects") {
			t.Fatalf("git count-objects stdout=%q, want objects count", outcome.Stdout)
		}
		if got := launches.Load(); got == 0 {
			t.Fatalf("git count-objects: launch spy recorded 0 launches, want >0")
		}
		if got := gapRows(); got != 1 {
			t.Fatalf("git count-objects: SQLite recorded %d native_gap rows, want 1", got)
		}
	})
}
