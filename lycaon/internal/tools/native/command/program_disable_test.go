package command_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

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
	testutil.FailErr(t, "seed readable file", os.WriteFile(filepath.Join(f.dir, "notes.txt"), []byte("notes\n"), 0o600))
	if stopped := f.runtime.SetDisabled(true); len(stopped) != 0 {
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
			if _, err := os.Lstat(filepath.Join(f.dir, "marker")); !os.IsNotExist(err) {
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
	testutil.FailErr(t, "seed readable file", os.WriteFile(filepath.Join(f.dir, "notes.txt"), []byte("notes\n"), 0o600))
	f.runtime.SetDisabled(true)
	args := map[string]any{"path": "notes.txt"}
	out, err := f.executor.Invoke(t.Context(), "read", args, f.modelCall(t, "read", args))
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
	f.runtime.SetDisabled(true)
	var settled, interrupted string
	testutil.FailErr(t, "read settled program", f.database.QueryRowContext(t.Context(), `SELECT id FROM invocation_programs WHERE state='completed' ORDER BY started_at LIMIT 1`).Scan(&settled))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for interrupted == "" && ctx.Err() == nil {
		if f.database.QueryRowContext(ctx, `SELECT id FROM invocation_programs WHERE state='interrupted'`).Scan(&interrupted) != nil {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if interrupted == "" {
		t.Fatal("disable did not settle the owned job's program")
	}
	for _, program := range []struct{ id, state string }{{settled, "completed"}, {interrupted, "interrupted"}} {
		page, err := f.recorder.ReadProgramPage(t.Context(), f.session, program.id, "", 20)
		testutil.FailErr(t, "read program page", err)
		if page.Program.State != program.state || len(page.Executions) == 0 {
			t.Fatalf("program %s rendered as %+v", program.id, page)
		}
		if _, err := json.Marshal(page); err != nil {
			t.Fatalf("program page does not encode: %v", err)
		}
	}
	list, err := f.recorder.ListSessionPage(t.Context(), f.session, "", 20)
	testutil.FailErr(t, "list session receipts", err)
	if len(list.Invocations) < 2 {
		t.Fatalf("session receipts after disable: %+v", list.Invocations)
	}
	if strings.TrimSpace(readFile(t, filepath.Join(f.dir, "kept.txt"))) != "before" {
		t.Fatal("disable changed an earlier program's published output")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read "+path, err)
	return string(data)
}
