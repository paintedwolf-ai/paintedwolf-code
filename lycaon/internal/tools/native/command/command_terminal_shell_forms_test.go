package command_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandTerminalCaptureMissingProgramUsesShellStatus(t *testing.T) {
	cmd, _ := newCommandTool(t)
	out, err := cmd.Run(context.Background(), map[string]any{
		"command":          "portable-command-missing-executable",
		"terminal_capture": map[string]any{},
	}, commandToolContext(t.TempDir(), "session-1", "worker-1"))
	testutil.FailErr(t, "capture missing program", err)
	var got struct {
		ExitCode int    `json:"exit_code"`
		OK       bool   `json:"ok"`
		Tail     string `json:"tail"`
	}
	testutil.FailErr(t, "decode capture", json.Unmarshal([]byte(out), &got))
	if got.ExitCode != 127 || got.OK || !strings.Contains(got.Tail, "bash: portable-command-missing-executable: command not found") {
		t.Fatalf("missing capture result: %s", out)
	}
}

func TestCommandTerminalCaptureExitedNonzeroReportsFailure(t *testing.T) {
	cmd, registry := newCommandTool(t)
	registry.Output.SetCaptureProjector(captureprojection.New(secretmatch.NewInertMatcher(), nil))
	out, err := cmd.Run(context.Background(), map[string]any{
		"command":          "/bin/sh -c 'exit 7'",
		"terminal_capture": map[string]any{},
	}, commandToolContext(t.TempDir(), "session-2", "worker-2"))
	testutil.FailErr(t, "capture exited program", err)
	var got struct {
		ExitCode int  `json:"exit_code"`
		OK       bool `json:"ok"`
	}
	testutil.FailErr(t, "decode capture", json.Unmarshal([]byte(out), &got))
	if got.ExitCode != 7 || got.OK {
		t.Fatalf("failed capture result: %s", out)
	}
}
