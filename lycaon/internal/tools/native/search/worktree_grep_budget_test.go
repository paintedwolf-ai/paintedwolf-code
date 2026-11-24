package search_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/search"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type commandGrepOutcome struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Status int    `json:"status"`
}

func newWorktreeGrepBudgetFixture(t *testing.T) (*programtest.Fixture, string, *tools.ToolInvocationOut) {
	t.Helper()
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	catalogBytes, err := os.ReadFile("../../../toolcontract/language_catalog.generated.go")
	testutil.FailErr(t, "read real language catalog", err)
	handlerBytes, err := os.ReadFile("../../../api/sourceapi/handler.go")
	testutil.FailErr(t, "read real sourceapi handler", err)

	var lastOut tools.ToolInvocationOut
	f := programtest.New(t, programtest.Options{
		Context: func(_ string, tc *tools.ToolContext) {
			lastOut = tools.ToolInvocationOut{}
			tc.Effects.Out = &lastOut
		},
	})

	// Seed the real 1.428 MB generated catalog, real sourceapi/handler.go, a later file for the
	// two-symbol alternation, and a multi-file directory with a genuinely over-budget VM record.
	overBudgetLine := strings.Repeat("a1b2c3d4", 55000) // 440,000 bytes without [X-Z]
	samplePrefix := "a1b2c3"
	files := map[string]string{
		"internal/toolcontract/language_catalog.generated.go": string(catalogBytes),
		"internal/api/sourceapi/handler.go":                   string(handlerBytes),
		"internal/zzz_later/tail_match.go":                    "package zzz_later\nconst symbol = \"LastAssistantMessageContent\"\n",
		"cmd/placeholder.go":                                  "package main\nfunc main() {}\n",
		"overbudget/a_early.txt":                              samplePrefix + "X\n",
		"overbudget/b_stop.txt":                               samplePrefix + "Y\n" + overBudgetLine + "\n" + samplePrefix + "Z\n",
		"overbudget/c_later.txt":                              samplePrefix + "X\n",
	}
	for rel, body := range files {
		abs := filepath.Join(f.Dir, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write "+rel, os.WriteFile(abs, []byte(body), 0o644))
	}

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", f.Dir}, args...)...)
		if out, gitErr := cmd.CombinedOutput(); gitErr != nil {
			t.Fatalf("git %v: %v\n%s", args, gitErr, string(out))
		}
	}
	runGit("init", "-q")
	runGit("add", ".")
	runGit("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "seed")

	home := os.Getenv("PW_TEST_HOST_HOME")
	if home == "" {
		home, err = os.UserHomeDir()
		testutil.FailErr(t, "locate home", err)
	}
	//nolint:usetesting // Linked worktree resides outside temporary scratch locations.
	outside, err := os.MkdirTemp(home, "paintedwolf-tp05-wt-*")
	testutil.FailErr(t, "mkdir outside", err)
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	outside, err = filepath.EvalSymlinks(outside)
	testutil.FailErr(t, "eval outside", err)
	wt := filepath.Join(outside, "sibling")
	runGit("worktree", "add", "-q", "-b", "feat/tp05-wt", wt)

	releaseGrant := projectpaths.SetGrantedAccessSource(func(_, _, abs string, write bool) (projectpaths.Access, bool) {
		if write {
			return projectpaths.Access{}, false
		}
		rel, relErr := filepath.Rel(wt, abs)
		if relErr == nil && (rel == "." || filepath.IsLocal(rel)) {
			return projectpaths.Access{Path: wt, Tree: true}, true
		}
		return projectpaths.Access{}, false
	})
	t.Cleanup(releaseGrant)

	catalog := sourcecatalog.New()
	t.Cleanup(func() { testutil.FailErr(t, "drain catalog", catalog.Drain(context.Background())) })
	searches, err := search.NewOwners(native.ExecutionRevisions{Boundary: f.Runtime.Boundary}, f.Recorder, nil)
	testutil.FailErr(t, "new search owners", err)
	searches.Catalog = catalog
	f.Runtime.Owners = func(_ tools.ToolContext, _ *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		programSearch := *searches
		programSearch.Revisions.Memory = memory
		programSearch.Admission = admission
		s := &programSearch
		natives := map[string]command.NativeOperation{}
		for _, name := range s.Commands() {
			tool, _ := s.Operation(name)
			natives[name] = command.NativeOperation{
				Tool: tool,
				SelectMode: func(_ context.Context, argv commandprogram.Fields) (string, bool) {
					return s.SelectMode(argv)
				},
				Prepare: func(_ context.Context, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
					prepared, prepErr := s.Prepare(paths, dispatch, request)
					if prepErr != nil {
						return command.NativePrepared{}, prepErr
					}
					return command.NativePrepared{Mode: search.EffectMode(prepared.Argv), Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
				},
			}
		}
		return command.ProgramOwners{Natives: natives}, nil
	}
	return f, wt, &lastOut
}

func callCommandGrep(t *testing.T, f *programtest.Fixture, prefix, cmd string) (commandGrepOutcome, error) {
	t.Helper()
	full := cmd
	if prefix != "" {
		full = prefix + " && " + cmd
	}
	raw, err := f.Call(t, t.Context(), full)
	if err != nil {
		return commandGrepOutcome{}, err
	}
	var out commandGrepOutcome
	testutil.FailErr(t, "decode command outcome", json.Unmarshal([]byte(raw), &out))
	return out, nil
}

func TestWorktreeAndAttachedCheckoutGrepCompletenessAndContinuation(t *testing.T) {
	f, wt, invOut := newWorktreeGrepBudgetFixture(t)

	for _, target := range []struct {
		name   string
		prefix string
		root   string
	}{
		{name: "linked_worktree", prefix: fmt.Sprintf("cd %q", wt), root: wt},
		{name: "attached_checkout", prefix: "", root: f.Dir},
	} {
		t.Run(target.name, func(t *testing.T) {
			// 1. Cold two-symbol alternation over real 1.428 MB catalog returns later file match.
			alt, err := callCommandGrep(t, f, target.prefix, `grep -rn -E "LastTurnMessageContent|LastAssistantMessageContent" internal cmd`)
			testutil.FailErr(t, "two-symbol alternation", err)
			if alt.Status != 0 || alt.Stderr != "" || !strings.Contains(alt.Stdout, "internal/zzz_later/tail_match.go:2:") {
				t.Fatalf("two-symbol alternation outcome = %+v", alt)
			}

			// 2. Command #71 cold fixed-string queries on unmodified tree + seeded positive control.
			miss, err := callCommandGrep(t, f, target.prefix, `grep -rn --include=*.go -F "sourceapi.Watch{" internal cmd`)
			testutil.FailErr(t, "sourceapi.Watch{ unmodified", err)
			if miss.Status != 1 || miss.Stdout != "" || miss.Stderr != "" {
				t.Fatalf("expected clean status 1 miss on unmodified tree, got %+v", miss)
			}

			hit, err := callCommandGrep(t, f, target.prefix, `grep -rn --include=*.go -F "&Watch{" internal/api`)
			testutil.FailErr(t, "&Watch{ control", err)
			if hit.Status != 0 || !strings.Contains(hit.Stdout, "internal/api/sourceapi/handler.go:") || !strings.Contains(hit.Stdout, "&Watch{") {
				t.Fatalf("expected handler.go &Watch{ hit, got %+v", hit)
			}

			seededPath := filepath.Join(target.root, "cmd/seeded_watch.go")
			testutil.FailErr(t, "seed watch file", os.WriteFile(seededPath, []byte("package main\nvar _ = sourceapi.Watch{}\n"), 0o644))
			seededHit, err := callCommandGrep(t, f, target.prefix, `grep -rn --include=*.go -F "sourceapi.Watch{" internal cmd`)
			testutil.FailErr(t, "sourceapi.Watch{ seeded", err)
			if seededHit.Status != 0 || !strings.Contains(seededHit.Stdout, "cmd/seeded_watch.go:2:") {
				t.Fatalf("expected seeded sourceapi.Watch{ hit, got %+v", seededHit)
			}

			// 3. Over-budget VM record: recursive search continues with status 2; single input refuses.
			var branches []string
			for _, ch := range []string{"a", "1", "b", "2", "c", "3", "d", "4", "e", "5"} {
				for _, end := range []string{"X", "Y", "Z"} {
					branches = append(branches, ch+".*"+end)
				}
			}
			heavyPattern := "(" + strings.Join(branches, "|") + ")"
			wantPrefix := "a1b2c3"
			recOut, err := callCommandGrep(t, f, target.prefix, fmt.Sprintf("grep -rn -E %q overbudget", heavyPattern))
			testutil.FailErr(t, "recursive overbudget grep", err)
			if recOut.Status != 2 ||
				!strings.Contains(recOut.Stdout, "overbudget/a_early.txt:1:"+wantPrefix+"X") ||
				!strings.Contains(recOut.Stdout, "overbudget/b_stop.txt:1:"+wantPrefix+"Y") ||
				!strings.Contains(recOut.Stdout, "overbudget/c_later.txt:1:"+wantPrefix+"X") ||
				strings.Contains(recOut.Stdout, wantPrefix+"Z") ||
				!strings.Contains(recOut.Stderr, "b_stop.txt: line 2 exceeds the search budget") {
				t.Fatalf("recursive overbudget outcome = %+v", recOut)
			}
			fb := invOut.Facts.FeedbackFor(toolrejection.GrepStoppedFilesCode)
			if !invOut.Facts.HasCode(toolrejection.GrepStoppedFilesCode) || fb.Details["grep_stop_line"] != 2 || fb.Details["grep_lines_emitted_before_stop"] != 1 || fb.Details["grep_stop_resource"] != "regex_work_steps" {
				t.Fatalf("expected GREP_STOPPED_FILES OAR feedback, got %+v", fb)
			}

			singleTarget := filepath.Join(target.root, "overbudget/b_stop.txt")
			singleOut, singleErr := callCommandGrep(t, f, "", fmt.Sprintf("grep -n -E %q %q", heavyPattern, singleTarget))
			testutil.FailErr(t, "single-input overbudget grep", singleErr)
			if singleOut.Status != 2 ||
				invOut.ProgramRefusal == nil ||
				invOut.ProgramRefusal.Code != "GREP_MATCH_BUDGET" ||
				invOut.Facts.HasCode(toolrejection.GrepStoppedFilesCode) ||
				!strings.Contains(singleOut.Stderr, "search limit regex_work_steps exceeded") {
				t.Fatalf("expected typed single-input GREP_MATCH_BUDGET refusal, out=%+v refusal=%+v", singleOut, invOut.ProgramRefusal)
			}
		})
	}

	if len(f.Delivered()) == 0 {
		t.Fatal("expected admitted source read deliveries to be recorded")
	}
	execStates := f.Executions(t)
	if !strings.Contains(execStates["grep"], "completed") {
		t.Fatalf("expected completed grep execution receipts, got %v", execStates)
	}
}
