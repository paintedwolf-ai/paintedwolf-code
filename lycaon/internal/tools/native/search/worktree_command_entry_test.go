package search_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/search"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type commandLogCapture struct {
	mu        sync.Mutex
	discovery []map[string]int64
	content   []map[string]int64
}

func (c *commandLogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *commandLogCapture) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *commandLogCapture) WithGroup(string) slog.Handler            { return c }
func (c *commandLogCapture) Handle(_ context.Context, record slog.Record) error {
	if record.Message != "command discovery completed" && record.Message != "command content search completed" {
		return nil
	}
	values := map[string]int64{}
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Value.Kind() == slog.KindInt64 {
			values[attr.Key] = attr.Value.Int64()
		}
		return true
	})
	c.mu.Lock()
	if record.Message == "command discovery completed" {
		c.discovery = append(c.discovery, values)
	} else {
		c.content = append(c.content, values)
	}
	c.mu.Unlock()
	return nil
}

func TestAgentCommandSearchesLinkedWorktreeColdAndWarm(t *testing.T) {
	logs := &commandLogCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(prev) })

	f := programtest.New(t, programtest.Options{})
	for i := range 16 {
		abs := filepath.Join(f.Dir, "src", fmt.Sprintf("neg_%02d.go", i))
		testutil.FailErr(t, "mkdir src", os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write neg", os.WriteFile(abs, []byte("package src\n// plain content\n"), 0o644))
	}
	matchAbs := filepath.Join(f.Dir, "src/target.go")
	testutil.FailErr(t, "write match", os.WriteFile(matchAbs, []byte("package src\n// LinkedWorktreeCommandNeedle\n"), 0o644))

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", f.Dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
	runGit("init", "-q")
	runGit("add", ".")
	runGit("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "seed")

	home := os.Getenv("PW_TEST_HOST_HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		testutil.FailErr(t, "locate home", err)
	}
	//nolint:usetesting // Linked worktree resides outside temporary scratch locations.
	outside, err := os.MkdirTemp(home, "paintedwolf-cmd-wt-*")
	testutil.FailErr(t, "mkdir outside", err)
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	outside, err = filepath.EvalSymlinks(outside)
	testutil.FailErr(t, "eval outside", err)
	wt := filepath.Join(outside, "sibling")
	runGit("worktree", "add", "-q", "-b", "feat/cmd-wt", wt)

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
			natives[name] = command.NativeOperation{Tool: tool, Prepare: func(_ context.Context, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, prepErr := s.Prepare(paths, dispatch, request)
				if prepErr != nil {
					return command.NativePrepared{}, prepErr
				}
				return command.NativePrepared{Mode: search.EffectMode(prepared.Argv), Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
			}}
		}
		return command.ProgramOwners{Natives: natives}, nil
	}

	invoke := func() string {
		t.Helper()
		raw, callErr := f.Call(t, t.Context(), fmt.Sprintf("cd %q && grep -rl LinkedWorktreeCommandNeedle src", wt))
		testutil.FailErr(t, "call command grep", callErr)
		var decoded struct {
			Stdout string `json:"stdout"`
			Status int    `json:"status"`
		}
		testutil.FailErr(t, "decode command output", json.Unmarshal([]byte(raw), &decoded))
		if decoded.Status != 0 {
			t.Fatalf("unexpected command status: %+v", decoded)
		}
		return strings.TrimSpace(decoded.Stdout)
	}

	coldOut := invoke()
	warmOut := invoke()
	if coldOut != "src/target.go" || warmOut != "src/target.go" {
		t.Fatalf("outputs cold=%q warm=%q", coldOut, warmOut)
	}
	logs.mu.Lock()
	defer logs.mu.Unlock()
	if len(logs.content) < 2 || len(logs.discovery) < 2 {
		t.Fatalf("missing search telemetry: discovery=%v content=%v", logs.discovery, logs.content)
	}
	coldContent := logs.content[0]
	warmContent := logs.content[1]
	warmDisc := logs.discovery[1]
	if coldContent["learned_files"] != 17 || warmContent["pruned_files"] != 16 || warmContent["captured_files"] != 1 {
		t.Fatalf("content telemetry cold=%v warm=%v", coldContent, warmContent)
	}
	if warmDisc["directory_reuses"] == 0 || warmDisc["directory_live_lists"] != 0 {
		t.Fatalf("warm discovery telemetry=%v", warmDisc)
	}
}
