package toolhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
)

type pipeSemanticsEnv struct {
	*programtest.Fixture
	scratchDir string
}

func newPipeSemanticsEnv(t *testing.T) *pipeSemanticsEnv {
	t.Helper()
	f := programtest.New(t, programtest.Options{})
	for _, name := range []string{"grep", "delete", "move", "copy", "mkdir", "chmod", "chown", "extract_archive", "jq_edit"} {
		testutil.FailErr(t, "register "+name, f.Runtime.Registry.Register(name, func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			return "", errors.New("structured tool is not exercised")
		}))
	}
	ops := mutation.Operations{
		Write:   &native.WriteTool{Boundary: f.Runtime.Boundary},
		Delete:  &native.DeleteTool{Boundary: f.Runtime.Boundary},
		Move:    &native.MoveTool{Boundary: f.Runtime.Boundary},
		Copy:    &native.CopyTool{Boundary: f.Runtime.Boundary},
		Mkdir:   &native.MkdirTool{Boundary: f.Runtime.Boundary},
		Chmod:   &native.ChmodTool{Boundary: f.Runtime.Boundary},
		Chown:   &native.ChownTool{Boundary: f.Runtime.Boundary},
		Times:   &native.TimesTool{Boundary: f.Runtime.Boundary},
		Extract: &native.ExtractArchiveTool{Boundary: f.Runtime.Boundary},
	}
	owners, err := (programOwnerTools{
		boundary:   f.Runtime.Boundary,
		operations: ops,
		git:        &git.Manager{},
		registry:   f.Runtime.Registry,
	}).compose(f.Recorder)
	testutil.FailErr(t, "compose program owners", err)
	f.Runtime.Owners = owners
	scratchDir := t.TempDir()
	f.Context = func(_ string, tc *tools.ToolContext) {
		tc.Host.SessionScratchDir = scratchDir
	}
	return &pipeSemanticsEnv{Fixture: f, scratchDir: scratchDir}
}

func decodePipeOutcome(t *testing.T, out string) command.ProgramOutcome {
	t.Helper()
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "decode program outcome", json.Unmarshal([]byte(out), &outcome))
	return outcome
}

// A native name answers as native even with an installed program of the same
// name on the launch PATH.
func TestProgramDiscoveryReportsWiredNativeUtilities(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	bin := t.TempDir()
	testutil.FailErr(t, "install jq", os.WriteFile(filepath.Join(bin, "jq"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	env.Configure = func(tc *tools.ToolContext) { tc.Host.HostResourcePathExtra = []string{bin} }
	for source, want := range map[string]string{
		"which jq":      "jq\n",
		"type jq":       "jq is a native built-in\n",
		"command -v jq": "jq\n",
	} {
		out, err := env.Call(t, t.Context(), source)
		testutil.FailErr(t, "run "+source, err)
		result := decodePipeOutcome(t, out)
		if result.Status != 0 || result.Refusal != nil || result.Stdout != want {
			t.Fatalf("%s: %+v, want stdout %q", source, result, want)
		}
	}
}

// TestPipeSemanticsSecretScreeningPreservesExactBytesAcrossPipesAndRedirects
// proves P07 stream screening applies only at model-facing output and retained
// child/program outputs, never on inter-stage pipes or file redirects.
func TestPipeSemanticsSecretScreeningPreservesExactBytesAcrossPipesAndRedirects(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build bundled secret matcher", err)
	env.Runtime.Projector = captureprojection.New(matcher, nil)

	awsKey := "AKIAQYJK5TXV4NZR7SGB"
	ghToken := "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3"
	for _, planted := range []string{awsKey, ghToken} {
		if len(matcher.Screen(planted)) == 0 {
			t.Fatal("planted secret must match the bundled screening policy")
		}
	}
	keyLine := "AWS_KEY=" + awsKey + " GITHUB_KEY=" + ghToken + "\n"
	fileBytes := []byte("header=plain\n" + keyLine + "footer=done\n")
	testutil.FailErr(t, "seed secret file", os.WriteFile(filepath.Join(env.Dir, "f"), fileBytes, 0o600))

	sum := sha256.Sum256(fileBytes)
	wantHash := hex.EncodeToString(sum[:])

	out, err := env.Call(t, t.Context(), "cat f | sha256sum")
	testutil.FailErr(t, "cat f | sha256sum", err)
	if outcome := decodePipeOutcome(t, out); outcome.Status != 0 || outcome.State != "completed" || outcome.Stdout != wantHash+"  -\n" {
		t.Fatalf("cat f | sha256sum outcome = %+v, want %s", outcome, wantHash)
	}

	out, err = env.Call(t, t.Context(), "cat f > copy && cmp f copy")
	testutil.FailErr(t, "cat f > copy && cmp f copy", err)
	if outcome := decodePipeOutcome(t, out); outcome.Status != 0 || outcome.State != "completed" || outcome.Stdout != "" {
		t.Fatalf("cat f > copy && cmp f copy outcome = %+v", outcome)
	}
	copied, err := os.ReadFile(filepath.Join(env.Dir, "copy"))
	testutil.FailErr(t, "read copy", err)
	if !bytes.Equal(copied, fileBytes) {
		t.Fatalf("redirected copy altered bytes: got %q, want %q", copied, fileBytes)
	}

	out, err = env.Call(t, t.Context(), "grep KEY f | wc -c")
	testutil.FailErr(t, "grep KEY f | wc -c", err)
	if outcome := decodePipeOutcome(t, out); outcome.Status != 0 || strings.TrimSpace(outcome.Stdout) != strconv.Itoa(len(keyLine)) {
		t.Fatalf("grep KEY f | wc -c = %q (status %d), want %d", outcome.Stdout, outcome.Status, len(keyLine))
	}

	out, err = env.Call(t, t.Context(), "cat f")
	testutil.FailErr(t, "cat f model-facing", err)
	outcome := decodePipeOutcome(t, out)
	if strings.Contains(outcome.Stdout, awsKey) || strings.Contains(outcome.Stdout, ghToken) || !strings.Contains(outcome.Stdout, "[REDACTED]") {
		t.Fatalf("model-facing stdout was not redacted: %q", outcome.Stdout)
	}

	var redactedBlobs int
	testutil.FailErr(t, "walk retained blobs", filepath.WalkDir(env.Runtime.Blobs.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if bytes.Contains(raw, []byte(awsKey)) || bytes.Contains(raw, []byte(ghToken)) {
			t.Fatalf("retained blob %s leaked secret bytes: %q", path, raw)
		}
		if bytes.Contains(raw, []byte("[REDACTED]")) {
			redactedBlobs++
		}
		return nil
	}))
	if redactedBlobs == 0 {
		t.Fatal("expected retained child outputs in blobstore to contain [REDACTED] markers")
	}
}

// TestPipeSemanticsStreamingAndEarlyTerminationWithBoundedMemory verifies that
// native and external producers stream incrementally and stop promptly with
// bounded memory when a downstream stage closes its input early.
func TestPipeSemanticsStreamingAndEarlyTerminationWithBoundedMemory(t *testing.T) {
	env := newPipeSemanticsEnv(t)

	var huge bytes.Buffer
	for i := 1; i <= 40000; i++ {
		fmt.Fprintf(&huge, "line-%06d match padding-0123456789abcdef\n", 40001-i)
	}
	testutil.FailErr(t, "seed huge", os.WriteFile(filepath.Join(env.Dir, "huge"), huge.Bytes(), 0o644))
	for _, sub := range []string{"a", "b", "c"} {
		testutil.FailErr(t, "mkdir "+sub, os.MkdirAll(filepath.Join(env.Dir, sub), 0o755))
		testutil.FailErr(t, "seed target "+sub, os.WriteFile(filepath.Join(env.Dir, sub, "x"), []byte("x\n"), 0o644))
	}

	var wantCatHead, wantSortHead strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&wantCatHead, "line-%06d match padding-0123456789abcdef\n", 40001-i)
		fmt.Fprintf(&wantSortHead, "line-%06d match padding-0123456789abcdef\n", i)
	}

	cases := []struct {
		name, program, want string
	}{
		{"native yes into head", "yes | head -1", "y\n"},
		{"external yes into head", "external -- /usr/bin/yes | head -1", "y\n"},
		{"seq hundred million into head", "seq 1 100000000 | head -3", "1\n2\n3\n"},
		{"cat huge into head", "cat huge | head", wantCatHead.String()},
		{"grep -m1 on huge", "grep -m1 match huge", "line-040000 match padding-0123456789abcdef\n"},
		{"find into head", "find . -name x | head -1", "./a/x\n"},
		{"sort huge into head", "sort huge | head", wantSortHead.String()},
		{"jq raw lines into head", "yes hello | jq -R . | head -1", "\"hello\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			out, err := env.Call(t, ctx, tc.program)
			testutil.FailErr(t, tc.name, err)
			outcome := decodePipeOutcome(t, out)
			if outcome.Status != 0 || outcome.State != "completed" || outcome.Stdout != tc.want {
				t.Fatalf("%s: status=%d state=%s stdout=%q, want %q", tc.name, outcome.Status, outcome.State, outcome.Stdout, tc.want)
			}
		})
	}
}

// TestPipeSemanticsIncrementalLineBufferedAndTerminalEnvironment verifies that
// downstream progress releases a waiting producer, and external commands see
// no TTY with TERM=dumb,
// PAGER=cat and GIT_PAGER=cat.
func TestPipeSemanticsIncrementalLineBufferedAndTerminalEnvironment(t *testing.T) {
	env := newPipeSemanticsEnv(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	out, err := env.Call(t, ctx, `external -- /bin/sh -c 'printf "=== RUN   TestFast\n--- PASS: TestFast\n"; while [ ! -f downstream.ready ]; do sleep .01; done' | grep --line-buffered PASS | external -- /bin/sh -c 'IFS= read -r line; printf "%s\n" "$line"; : > downstream.ready'`)
	testutil.FailErr(t, "incremental line-buffered pipeline", err)
	outcome := decodePipeOutcome(t, out)
	if outcome.Status != 0 || outcome.Stdout != "--- PASS: TestFast\n" {
		t.Fatalf("incremental pipeline = %+v", outcome)
	}

	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	out, err = env.Call(t, t.Context(), `external -- /bin/sh -c '[ ! -t 0 ] && [ ! -t 1 ] && [ ! -t 2 ] && printf "TERM=%s PAGER=%s GIT_PAGER=%s COLORTERM=%s\n" "$TERM" "$PAGER" "$GIT_PAGER" "$COLORTERM"'`)
	testutil.FailErr(t, "terminal and env probe", err)
	if outcome = decodePipeOutcome(t, out); outcome.Status != 0 || outcome.Stdout != "TERM=dumb PAGER=cat GIT_PAGER=cat COLORTERM=\n" {
		t.Fatalf("terminal/env probe = %+v", outcome)
	}

	bin, err := gitengine.BinaryPath()
	testutil.FailErr(t, "bundled git", err)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "Ann"}, {"config", "user.email", "ann@example.test"}} {
		cmd := exec.Command(bin, args...)
		cmd.Dir = env.Dir
		testutil.FailErr(t, "git "+args[0], cmd.Run())
	}
	testutil.FailErr(t, "seed tracked", os.WriteFile(filepath.Join(env.Dir, "tracked.txt"), []byte("v1\n"), 0o644))
	for _, args := range [][]string{{"add", "tracked.txt"}, {"commit", "-q", "-m", "initial commit"}} {
		cmd := exec.Command(bin, args...)
		cmd.Dir = env.Dir
		testutil.FailErr(t, "git "+args[0], cmd.Run())
	}

	out, err = env.Call(t, t.Context(), "git log -n 1 --format=%s && external -- "+bin+" log -n 1 --format=%s && ls --color=auto tracked.txt")
	testutil.FailErr(t, "git log and ls --color=auto", err)
	if outcome = decodePipeOutcome(t, out); outcome.Status != 0 || outcome.Stdout != "initial commit\ninitial commit\ntracked.txt\n" || strings.Contains(outcome.Stdout, "\x1b") {
		t.Fatalf("git log / ls output = %+v", outcome)
	}
}
