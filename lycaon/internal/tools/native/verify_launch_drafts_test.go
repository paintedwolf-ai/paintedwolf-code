package native_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestVerificationEvidenceCoversPublishedLaunchDrafts(t *testing.T) {
	for _, tc := range []struct {
		name, command, stdout, verdict, capture string
		secondPublication                       bool
		exitCode                                int
	}{
		{name: "verify", command: "external -- /bin/cat input.txt; external -- /bin/cat input.txt", stdout: "first\nfirst\n", verdict: api.SourceVerdictPassed},
		{name: "verify across two revisions", command: "external -- /bin/cat input.txt; external -- /bin/cat input.txt", stdout: "first\nsecond\n", verdict: api.SourceVerdictUnverifiable, secondPublication: true},
		{name: "terminal capture", command: "/bin/cat input.txt", verdict: api.SourceVerdictPassed, capture: "terminal_capture"},
		{name: "snapshot capture", command: "./read-check.sh", verdict: api.SourceVerdictFailed, capture: "snapshot_capture", exitCode: 42},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := programtest.New(t, programtest.Options{Tools: []string{"verify"}})
			ledger := sourceledger.New(f.Database, t.TempDir())
			drafts := &testfixture.DiskDrafts{Publish: func(path, text string) error {
				if err := os.WriteFile(filepath.Join(f.Dir, path), []byte(text), 0o644); err != nil {
					return err
				}
				repochange.Advance(f.Dir)
				repochange.Notify(t.Context(), repochange.Event{
					ProjectDir: f.Dir, Kind: repochange.WorktreeChanged, Paths: []string{path}, Source: repochange.SourceMutation,
				})
				return nil
			}}
			testutil.FailErr(t, "write initial input", os.WriteFile(filepath.Join(f.Dir, "input.txt"), []byte("saved\n"), 0o644))
			if tc.exitCode != 0 {
				script := "#!/bin/sh\nread -r line < input.txt\n[ \"$line\" = first ] && exit 42\nexit 43\n"
				testutil.FailErr(t, "write source-check process", os.WriteFile(filepath.Join(f.Dir, "read-check.sh"), []byte(script), 0o755))
			}
			drafts.Type("input.txt", "first\n")
			if tc.secondPublication {
				drafts.OnSave = func() {
					if len(drafts.Calls()) == 1 {
						drafts.Type("input.txt", "second\n")
					}
				}
			}
			out := &tools.ToolInvocationOut{}
			f.Configure = func(tc *tools.ToolContext) {
				tc.Source.EditorDocuments = drafts
				tc.Source.Observations = ledger.Inventory
				tc.Effects.Out = out
			}
			before, _ := sourceledger.VerificationState(t.Context(), ledger.Inventory, f.Dir)
			if before == "" {
				t.Fatal("initial source identity unavailable")
			}

			args := map[string]any{"command": tc.command}
			tool, handler := "verify", (&native.VerifyTool{Programs: f.Runtime}).Run
			if tc.capture != "" {
				args["verification"], args[tc.capture] = true, map[string]any{}
				tool, handler = "command", (&command.CommandTool{Runner: hostcmd.NewRunner(), Boundary: f.Boundary, Background: f.Background}).Run
			}
			raw, err := f.Invoke(t, t.Context(), tool, args, handler)
			testutil.FailErr(t, "run check", err)
			if tool == "verify" {
				assertVerificationLaunchResult(t, raw, tc.stdout, tc.verdict)
			}
			after, root := sourceledger.VerificationState(t.Context(), ledger.Inventory, f.Dir)
			if after == "" || after == before || root == "" {
				t.Fatalf("draft did not change the source identity: before=%q after=%q root=%q", before, after, root)
			}
			if tc.secondPublication {
				after, root = "", ""
			}
			if out.SourceRun == nil || out.SourceRun.Verdict != tc.verdict || out.SourceRun.ExitCode != tc.exitCode || out.SourceRun.SourceRevision != after || out.SourceRun.SourceRootDigest != root {
				t.Fatalf("evidence=%+v, want %s for %q at %q", out.SourceRun, tc.verdict, after, root)
			}
		})
	}
}

func assertVerificationLaunchResult(t *testing.T, raw, stdout, verdict string) {
	t.Helper()
	var result command.ProgramOutcome
	testutil.FailErr(t, "decode check", json.Unmarshal([]byte(raw), &result))
	if result.Status != 0 || result.Stdout != stdout || result.Outcome != verdict {
		t.Fatalf("check output = %s", raw)
	}
}
