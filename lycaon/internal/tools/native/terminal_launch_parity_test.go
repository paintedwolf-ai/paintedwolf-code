//go:build !windows

package native

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/terminal"
)

func TestTerminalAndCommandShareLaunchEnvironmentAndScratch(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "0")
	t.Setenv("LYCAON_SANDBOX", "on")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "deny")
	t.Setenv("TERMINAL_ENGINE_CANARY", "private-fixture-value")
	scratch, data := t.TempDir(), t.TempDir()
	var executableDir string
	var tc tools.ToolContext
	f := programtest.New(t, programtest.Options{Context: func(_ string, current *tools.ToolContext) {
		current.Host.SessionScratchDir = scratch
		current.Host.HostDataDir = data
		current.Host.HostResourcePathExtra = []string{executableDir}
		tc = *current
	}})
	executableDir = filepath.Join(f.Dir, "bin")
	testutil.FailErr(t, "fixture executable directory", os.Mkdir(executableDir, 0o755))
	probe := `#!/bin/sh
printf '%s\n' "${TERMINAL_ENGINE_CANARY-unset}" "$TERMINAL_FIXTURE_VALUE" "$SCRATCH_DIR" "$TMPDIR" "$TMP" "$TEMP" "$PWD" > "$1"
printf '%s' "$$" > "$1.pid"
if [ -t 0 ] && [ -t 1 ]; then
    printf 'tty\n' >> "$1"
    printf 'terminal ready\n'
    IFS= read -r input
    printf '%s' "$input" > sent.txt
else
    printf 'pipe\n' >> "$1"
fi
`
	testutil.FailErr(t, "fixture executable", os.WriteFile(filepath.Join(executableDir, "terminal-launch-probe"), []byte(probe), 0o755))
	raw, err := f.Call(t, t.Context(), "TERMINAL_FIXTURE_VALUE=provided terminal-launch-probe command.txt")
	testutil.FailErr(t, "command external launch", err)
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "command outcome", json.Unmarshal([]byte(raw), &outcome))
	if outcome.Status != 0 || outcome.Refusal != nil {
		t.Fatalf("command launch=%+v", outcome)
	}
	f.Background.Output.SetCaptureProjector(f.Runtime.Projector)
	registry := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register terminal owners", RegisterTerminalSessionTools(registry, f.Background))
	tc.Identity.ToolCallID = "terminal-launch-fixture"
	tc.Invocation.ID = ""
	tc.Effects.Out = &tools.ToolInvocationOut{}
	raw, err = registry.Run(t.Context(), terminal.OpenToolName, map[string]any{
		"command": "terminal-launch-probe terminal.txt", "env": map[string]any{"TERMINAL_FIXTURE_VALUE": "provided"},
	}, tc)
	testutil.FailErr(t, "terminal launch", err)
	var opened terminal.OpenResult
	testutil.FailErr(t, "terminal outcome", json.Unmarshal([]byte(raw), &opened))
	t.Cleanup(func() { _, _ = f.Background.Terminal.ClosePTY(tc.Identity.SessionID, opened.ID) })
	if !opened.Confined || opened.Snapshot == nil || opened.Width == 0 || opened.Height == 0 || tc.Effects.Out.Visual == nil || len(tc.Effects.Out.Visual.Bytes) == 0 {
		t.Fatalf("terminal launch lacks confined screen evidence: %+v", opened)
	}
	for _, route := range []string{"command", "terminal"} {
		body, err := os.ReadFile(filepath.Join(f.Dir, route+".txt"))
		testutil.FailErr(t, route+" launch observations", err)
		got := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
		if len(got) != 8 || !filepath.IsAbs(got[6]) {
			t.Fatalf("%s launch facts=%q", route, got)
		}
		physical, err := filepath.EvalSymlinks(got[6])
		testutil.FailErr(t, route+" physical working directory", err)
		expectedDir, err := filepath.EvalSymlinks(f.Dir)
		testutil.FailErr(t, "fixture physical working directory", err)
		got[6] = physical
		want := []string{"unset", "provided", scratch, scratch, scratch, scratch, expectedDir, "pipe"}
		if route == "terminal" {
			want[7] = "tty"
		}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("%s launch facts=%q, want %q", route, got, want)
		}
	}
	pidBytes, err := os.ReadFile(filepath.Join(f.Dir, "terminal.txt.pid"))
	testutil.FailErr(t, "terminal process identity", err)
	pid, err := strconv.Atoi(string(pidBytes))
	testutil.FailErr(t, "terminal process PID", err)
	members, err := lineage.SessionMembers(tc.Identity.SessionID)
	testutil.FailErr(t, "terminal session lineage members", err)
	if _, ok := members[pid]; !ok {
		for memberPID := range members {
			if memberPID > pid {
				pid = memberPID
			}
		}
	}
	processes, err := hostprocess.New()
	testutil.FailErr(t, "session process owner", err)
	listed, err := processes.List(t.Context(), tc.Identity.SessionID, pid, 0, 1)
	testutil.FailErr(t, "inspect owned terminal process", err)
	if len(listed.Processes) != 1 || !listed.Processes[0].Owned || listed.Processes[0].Reference == "" {
		t.Fatalf("terminal not registered with session process owner: %+v", listed)
	}
	process := listed.Processes[0]
	testutil.FailErr(t, "signal own terminal", processes.Signal(t.Context(), tc.Identity.SessionID, process, "CONT"))
	if err := processes.Signal(t.Context(), "another-session", process, "CONT"); !errors.Is(err, hostprocess.ErrNotOwned) {
		t.Fatalf("foreign session terminal signal=%v", err)
	}
	if _, err := f.Background.Terminal.ClosePTY("another-session", opened.ID); err == nil {
		t.Fatal("another session closed the terminal")
	}
	_, err = registry.Run(t.Context(), terminal.SendToolName, map[string]any{"id": opened.ID, "input": "interactive{Enter}"}, tc)
	testutil.FailErr(t, "send terminal input", err)
	_, err = registry.Run(t.Context(), terminal.ReadToolName, map[string]any{"id": opened.ID, "idle_ms": 100.0, "timeout_ms": 2000.0}, tc)
	testutil.FailErr(t, "observe terminal completion", err)
	body, err := os.ReadFile(filepath.Join(f.Dir, "sent.txt"))
	testutil.FailErr(t, "terminal received input", err)
	if string(body) != "interactive" {
		t.Fatalf("terminal input=%q", body)
	}
	_, err = registry.Run(context.Background(), terminal.CloseToolName, map[string]any{"id": opened.ID}, tc)
	testutil.FailErr(t, "close own terminal", err)
}
