package command_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func newProgramFixtureWith(t *testing.T, structured map[string]tools.ToolHandler) *programFixture {
	t.Helper()
	f := newProgramFixture(t)
	registry := tools.NewDefaultRegistry()
	for _, name := range []string{"write", "read"} {
		handler := structured[name]
		if handler == nil {
			handler = func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				return "", errors.New("structured tool is not exercised")
			}
		}
		testutil.FailErr(t, "register "+name, registry.Register(name, handler))
	}
	classes, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec)
	testutil.FailErr(t, "compile profile effects", err)
	offered := map[string]bool{}
	for name := range structured {
		offered[name] = true
	}
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "fixture", Tools: offered, CommandEffects: classes}})
	f.Runtime.Registry = registry
	f.Runtime.Boundary = boundary
	f.Runtime.Executor = toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), registry, "fixture")
	return f
}

func (f *programFixture) modelCall(t *testing.T, tool string, args map[string]any) tools.ToolContext {
	t.Helper()
	callID := fmt.Sprintf("model-call-%s", tool)
	contract, _ := toolcontract.Lookup(tool)
	parent, err := f.Recorder.Begin(t.Context(), invocation.Start{ProjectID: testdbseed.DefaultProjectID, SessionID: f.Session, ToolCallID: callID, ToolName: tool, Args: args, Contract: contract})
	testutil.FailErr(t, "begin model receipt", err)
	tc := tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: f.Dir, IsPrimary: true}}, ActiveRootID: "r1", SourceWorkspaceKind: api.SourceWorkspaceKindProject}}
	tc.Identity.Agent, tc.Identity.ProjectID, tc.Identity.SessionID, tc.Identity.ToolCallID = "fixture", testdbseed.DefaultProjectID, f.Session, callID
	tc.Invocation.ID = parent.ID
	tc.Host.SessionScratchDir, tc.Host.SessionOutputDir = f.ScratchDir, f.OutputDir
	return tc
}

// A disabled command entry refuses every program before evaluation: nothing is
// recorded, no process starts and no structured owner is reached on its behalf.
func TestProgramEntryDisabledRefusesWithoutShellFallback(t *testing.T) {
	var structuredCalls int
	f := newProgramFixtureWith(t, map[string]tools.ToolHandler{
		"read": func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			structuredCalls++
			return "structured read", nil
		},
	})
	testutil.FailErr(t, "seed readable file", os.WriteFile(filepath.Join(f.Dir, "notes.txt"), []byte("notes\n"), 0o600))
	if stopped := f.Runtime.SetDisabled(true); len(stopped) != 0 {
		t.Fatalf("disable with no owned work stopped %v", stopped)
	}
	for _, source := range []string{
		"external -- /usr/bin/touch marker",
		"external -- /bin/sh -c 'touch marker'",
		"echo created > marker",
		"cat notes.txt | tee marker",
		"external -- /bin/sleep 30 &",
	} {
		t.Run(source, func(t *testing.T) {
			out, err := f.call(t, t.Context(), source)
			if rejected := toolrejection.AsToolReject(err); rejected == nil || rejected.Code != "COMMAND_PROGRAMS_DISABLED" {
				t.Fatalf("disabled entry returned %q %v", out, err)
			}
			if out != "" {
				t.Fatalf("disabled entry produced output %q", out)
			}
			if _, err := os.Lstat(filepath.Join(f.Dir, "marker")); !os.IsNotExist(err) {
				t.Fatalf("disabled program reached the filesystem: %v", err)
			}
			if programs, executions := f.programRows(t), f.executions(t); len(programs) != 0 || len(executions) != 0 {
				t.Fatalf("disabled program recorded programs %v executions %v", programs, executions)
			}
			if structuredCalls != 0 {
				t.Fatalf("disabled program dispatched %d structured calls", structuredCalls)
			}
		})
	}
}

// Disabling programs leaves the retained structured tools dispatchable through
// the same executor.
func TestProgramEntryDisabledKeepsStructuredToolsDispatchable(t *testing.T) {
	var seen []string
	f := newProgramFixtureWith(t, map[string]tools.ToolHandler{
		"read": func(_ context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
			path, _ := args["path"].(string)
			seen = append(seen, path)
			return "structured read " + path, nil
		},
	})
	testutil.FailErr(t, "seed readable file", os.WriteFile(filepath.Join(f.Dir, "notes.txt"), []byte("notes\n"), 0o600))
	f.Runtime.SetDisabled(true)
	args := map[string]any{"path": "notes.txt"}
	out, err := f.Runtime.Executor.Invoke(t.Context(), "read", args, f.modelCall(t, "read", args))
	testutil.FailErr(t, "structured read while programs are disabled", err)
	if !strings.Contains(out, "structured read") || len(seen) != 1 || !strings.HasSuffix(seen[0], "notes.txt") {
		t.Fatalf("structured read returned %q after %v", out, seen)
	}
	if _, err := f.call(t, t.Context(), "cat notes.txt"); toolrejection.AsToolReject(err) == nil {
		t.Fatalf("disabled entry admitted a program after structured dispatch: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("refused program reached the structured owner: %v", seen)
	}
}

// Programs settled before the control was turned off keep their receipts:
// the session receipt list and the program page the API serves still read.
func TestProgramEntryDisabledKeepsSettledReceiptsReadable(t *testing.T) {
	f := newProgramFixture(t)
	_, err := f.call(t, t.Context(), "external -- /bin/echo before | tee kept.txt")
	testutil.FailErr(t, "run program before disabling", err)
	_, err = f.call(t, t.Context(), "external -- /bin/sleep 30 &")
	testutil.FailErr(t, "launch owned job", err)
	f.Runtime.SetDisabled(true)
	var settled, interrupted string
	testutil.FailErr(t, "read settled program", f.Database.QueryRowContext(t.Context(), `SELECT id FROM invocation_programs WHERE state='completed' ORDER BY started_at LIMIT 1`).Scan(&settled))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for interrupted == "" && ctx.Err() == nil {
		if f.Database.QueryRowContext(ctx, `SELECT id FROM invocation_programs WHERE state='interrupted'`).Scan(&interrupted) != nil {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if interrupted == "" {
		t.Fatal("disable did not settle the owned job's program")
	}
	for _, program := range []struct{ id, state string }{{settled, "completed"}, {interrupted, "interrupted"}} {
		page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, program.id, "", 20)
		testutil.FailErr(t, "read program page", err)
		if page.Program.State != program.state || len(page.Executions) == 0 {
			t.Fatalf("program %s rendered as %+v", program.id, page)
		}
		if _, err := json.Marshal(page); err != nil {
			t.Fatalf("program page does not encode: %v", err)
		}
	}
	list, err := f.Recorder.ListSessionPage(t.Context(), f.Session, "", 20)
	testutil.FailErr(t, "list session receipts", err)
	if len(list.Invocations) < 2 {
		t.Fatalf("session receipts after disable: %+v", list.Invocations)
	}
	if strings.TrimSpace(readFile(t, filepath.Join(f.Dir, "kept.txt"))) != "before" {
		t.Fatal("disable changed an earlier program's published output")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read "+path, err)
	return string(data)
}
