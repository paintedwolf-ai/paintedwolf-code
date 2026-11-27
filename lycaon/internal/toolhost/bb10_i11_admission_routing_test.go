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
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// TestBB10I11AdmissionSurvivesRouting verifies BB10 I11 across direct native
// dispatch, nested dispatch, pipeline dispatch, and structured form-tool
// adaptation with independent dispatch/launch spies and persisted receipts.
func TestBB10I11AdmissionSurvivesRouting(t *testing.T) {
	bin, err := gitengine.BinaryPath()
	testutil.FailErr(t, "require pinned Git", err)
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	f := newComposedProgramFixture(t)
	origOwners := f.Runtime.Owners
	f.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, mem *toolcontract.ProgramMemory, admit tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		owners, err := origOwners(tc, paths, mem, admit)
		if err != nil {
			return owners, err
		}
		gitOp := owners.Natives["git"]
		gitOp.Tools = []string{"git_status"}
		owners.Natives["git"] = gitOp
		return owners, nil
	}

	docs := newStubEditorDocs()
	var launches atomic.Int64
	var rootID string
	configure := f.Configure
	f.Configure = func(tc *tools.ToolContext) {
		if configure != nil {
			configure(tc)
		}
		rootID = tc.Source.ActiveRootID
		tc.Source.EditorDocuments = docs
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
	runGit("config", "user.name", "BB10 Author")
	runGit("config", "user.email", "bb10@example.com")
	seedFile := filepath.Join(f.Dir, "seed.txt")
	testutil.FailErr(t, "seed.txt", os.WriteFile(seedFile, []byte("seed\n"), 0o644))
	runGit("add", "seed.txt")
	runGit("commit", "-m", "init")
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

	t.Run("terminal_policy_refusal_survives_direct_nested_and_pipeline_routes", func(t *testing.T) {
		for _, tc := range []struct {
			name, source string
		}{
			{"direct", `git -c core.fsmonitor=fixture-inert status`},
			{"nested_env", `env git -c core.fsmonitor=fixture-inert status`},
			{"pipeline", `git -c core.fsmonitor=fixture-inert status | cat`},
			{"nested_unknown_flag", `env git -c core.fsmonitor=fixture-inert status --unknown-flag`},
			{"unserved_subcommand", `git -c core.fsmonitor=fixture-inert count-objects`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				launches.Store(0)
				outcome := callComposedProgram(t, f, tc.source)
				if outcome.Refusal == nil || outcome.Refusal.Code != "COMMAND_OPTION_UNSUPPORTED" || outcome.NativeGaps != 0 {
					t.Fatalf("%s: outcome=%+v refusal=%+v", tc.name, outcome, outcome.Refusal)
				}
				if got := launches.Load(); got != 0 {
					t.Fatalf("%s: prohibited route launched %d processes, want 0", tc.name, got)
				}
				if got := gapRows(); got != 0 {
					t.Fatalf("%s: recorded %d native_gap rows, want 0", tc.name, got)
				}
				afterCfg, err := os.ReadFile(cfgPath)
				testutil.FailErr(t, "read .git/config after", err)
				if string(afterCfg) != string(beforeCfg) {
					t.Fatalf("%s: .git/config mutated", tc.name)
				}
			})
		}
	})

	t.Run("admitted_native_op_and_eligible_unsupported_fallback_across_routes", func(t *testing.T) {
		for _, source := range []string{
			`git -c core.fsmonitor=false status`,
			`env git -c core.fsmonitor=false status`,
		} {
			launches.Store(0)
			outcome := callComposedProgram(t, f, source)
			if outcome.Refusal != nil || outcome.Status != 0 || outcome.NativeGaps != 0 || launches.Load() == 0 {
				t.Fatalf("%q: outcome=%+v refusal=%+v launches=%d", source, outcome, outcome.Refusal, launches.Load())
			}
		}
		for _, source := range []string{`git count-objects`, `env git count-objects`} {
			launches.Store(0)
			beforeGaps := gapRows()
			outcome := callComposedProgram(t, f, source)
			if outcome.Refusal != nil || outcome.Status != 0 || outcome.NativeGaps != 1 || launches.Load() == 0 {
				t.Fatalf("%q: outcome=%+v refusal=%+v launches=%d", source, outcome, outcome.Refusal, launches.Load())
			}
			if !strings.Contains(outcome.Stdout, "objects") || gapRows() != beforeGaps+1 {
				t.Fatalf("%q: stdout=%q gaps=%d (before %d)", source, outcome.Stdout, gapRows(), beforeGaps)
			}
		}
	})

	t.Run("structured_adapter_preserves_conversational_read_base_while_native_command_stays_pinned", func(t *testing.T) {
		initial := "package main\n\nfunc f() string { return wrap(\"x\") }\n"
		openPath := filepath.Join(f.Dir, "i11_open.go")
		testutil.FailErr(t, "seed i11_open.go", os.WriteFile(openPath, []byte(initial), 0o644))
		docs.addOpen(rootID, "i11_open.go", openPath, "doc-i11-open", initial)

		// Unread open doc refuses through structured adapter with zero mutation.
		refused := runFormTool(t, f, "code_rewrite", toolreach.CodeRewriteArgv, map[string]any{
			"path": "i11_open.go", "pattern": "wrap($A)", "rewrite": "unwrap($A)",
		})
		if refused.Refusal == nil || refused.Refusal.Code != "EDITOR_DOCUMENT_READ_REQUIRED" || refused.CompletedEffects != 0 || docs.applyCalls != 0 {
			t.Fatalf("structured unread rewrite: outcome=%+v refusal=%+v apply=%d", refused, refused.Refusal, docs.applyCalls)
		}

		// Whole-document read authorizes the structured adapter.
		readOut := callComposedProgram(t, f, `cat i11_open.go`)
		if readOut.Status != 0 || readOut.Stdout != initial {
			t.Fatalf("whole read failed: %+v", readOut)
		}
		okOut := runFormTool(t, f, "code_rewrite", toolreach.CodeRewriteArgv, map[string]any{
			"path": "i11_open.go", "pattern": "wrap($A)", "rewrite": "unwrap($A)",
		})
		if okOut.Refusal != nil || okOut.State != "completed" || okOut.CompletedEffects != 1 || docs.docByPath(rootID, "i11_open.go").Revision != 2 {
			t.Fatalf("structured read rewrite: outcome=%+v refusal=%+v rev=%d", okOut, okOut.Refusal, docs.docByPath(rootID, "i11_open.go").Revision)
		}

		// Native command stage stays execution-pinned on a separate unread open doc.
		pinnedPath := filepath.Join(f.Dir, "i11_pinned.go")
		testutil.FailErr(t, "seed i11_pinned.go", os.WriteFile(pinnedPath, []byte(initial), 0o644))
		docs.addOpen(rootID, "i11_pinned.go", pinnedPath, "doc-i11-pinned", initial)
		cmdOut := callComposedProgram(t, f, `code-rewrite --path i11_pinned.go --pattern 'wrap($A)' --rewrite 'unwrap($A)'`)
		if cmdOut.Refusal != nil || cmdOut.State != "completed" || docs.docByPath(rootID, "i11_pinned.go").Revision != 2 {
			t.Fatalf("native command code-rewrite: outcome=%+v refusal=%+v rev=%d", cmdOut, cmdOut.Refusal, docs.docByPath(rootID, "i11_pinned.go").Revision)
		}
	})
}
