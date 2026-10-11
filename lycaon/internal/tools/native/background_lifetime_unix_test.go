//go:build unix

package native_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

func TestBackgroundHandleOutlivesInvocationAndStopsThroughTool(t *testing.T) {
	tool, registry := newCommandTool(t)
	registry.Output.SetCaptureProjector(captureprojection.New(secretmatch.NewInertMatcher(), nil))
	t.Cleanup(func() { _ = registry.Lifecycle.Close(t.Context()) })
	tctx := commandToolContext(t.TempDir(), "owner", "")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	raw, err := tool.Run(ctx, map[string]any{
		"command": "sh -c 'printf background-ready; sleep 30'", "background": true,
	}, tctx)
	testutil.FailErr(t, "start background command", err)
	var started hostcmd.BackgroundStartResult
	testutil.FailErr(t, "decode background handle", json.Unmarshal([]byte(raw), &started))
	if started.Handle == "" || started.ExitedEarly {
		t.Fatalf("live command was not promoted: %s", raw)
	}
	cancel()
	outputTool := &command.CommandOutputTool{Registry: registry}
	testutil.WaitFor(t, 5*time.Second, func() bool {
		output, readErr := outputTool.Run(t.Context(), map[string]any{"handle": started.Handle}, tctx)
		return readErr == nil && strings.Contains(output, "background-ready")
	})
	if !registry.HasRunning("owner") {
		t.Fatal("invocation cancellation terminated the background handle")
	}
	_, err = (&command.CommandStopTool{Registry: registry}).Run(t.Context(), map[string]any{"handle": started.Handle}, tctx)
	testutil.FailErr(t, "stop background through public tool", err)
	done, err := registry.Lifecycle.Await(t.Context(), "owner", started.Handle, 5*time.Second)
	testutil.FailErr(t, "await stopped background command", err)
	if !done || registry.HasRunning("owner") {
		t.Fatal("stop tool did not settle the background command")
	}
}

func TestBackgroundFastExitIsReportedInlineWithRetainedOutput(t *testing.T) {
	tool, registry := newCommandTool(t)
	registry.Output.SetCaptureProjector(captureprojection.New(secretmatch.NewInertMatcher(), nil))
	t.Cleanup(func() { _ = registry.Lifecycle.Close(t.Context()) })
	raw, err := tool.Run(t.Context(), map[string]any{
		"command": "printf early-output", "background": true,
	}, commandToolContext(t.TempDir(), "owner", ""))
	testutil.FailErr(t, "run fast background command", err)
	var result hostcmd.BackgroundStartResult
	testutil.FailErr(t, "decode inline completion", json.Unmarshal([]byte(raw), &result))
	if !result.ExitedEarly || result.ExitCode == nil || *result.ExitCode != 0 || !strings.Contains(result.Tail, "early-output") {
		t.Fatalf("fast exit lost terminal state or output: %s", raw)
	}
	if registry.HasRunning("owner") {
		t.Fatal("inline background completion retained a live handle")
	}
}
