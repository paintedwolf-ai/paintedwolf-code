package command_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

func TestBackgroundLaunchSettlesFastExitWithoutLiveIndicator(t *testing.T) {
	tool, reg := newCommandTool(t)
	ctx := commandToolContext(t.TempDir(), "background-fast", "")
	captured := &tools.ToolInvocationOut{}
	ctx.Out = captured
	raw, err := command.RunBackground(t.Context(), reg, tool.Runner, tool.Boundary,
		map[string]any{"command": "printf background-complete"}, ctx, nil, "command")
	testutil.FailErr(t, "launch background command", err)
	var start hostcmd.BackgroundStartResult
	testutil.FailErr(t, "decode launch", json.Unmarshal([]byte(raw), &start))
	if !start.Background || !start.ExitedEarly || start.ExitCode == nil || *start.ExitCode != 0 || !strings.Contains(start.Tail, "background-complete") {
		t.Fatalf("completed launch = %+v", start)
	}
	if captured.Process == nil || captured.Process.Handle != start.Handle || captured.Process.Running || reg.HasRunning(ctx.SessionID) {
		t.Fatalf("completed launch retained live status: %+v", captured.Process)
	}
	output := &command.CommandOutputTool{Registry: reg}
	raw, err = output.Run(t.Context(), map[string]any{"handle": start.Handle}, ctx)
	testutil.FailErr(t, "read retained completed output", err)
	if !strings.Contains(raw, "background-complete") || !strings.Contains(raw, `"running":false`) {
		t.Fatalf("completed output = %s", raw)
	}
}

func TestBackgroundHandleRetainsOutputAcrossCancellationAndEnforcesSessionOwnership(t *testing.T) {
	tool, reg := newCommandTool(t)
	ctx := commandToolContext(t.TempDir(), "background-live", "")
	captured := &tools.ToolInvocationOut{}
	ctx.Out = captured
	request, cancel := context.WithCancel(t.Context())
	defer cancel()
	raw, err := command.RunBackground(request, reg, tool.Runner, tool.Boundary,
		map[string]any{"command": "printf first-output; sleep 10"}, ctx, nil, "command")
	testutil.FailErr(t, "launch persistent background command", err)
	var start hostcmd.BackgroundStartResult
	testutil.FailErr(t, "decode persistent launch", json.Unmarshal([]byte(raw), &start))
	t.Cleanup(func() {
		testutil.FailErr(t, "dispose background session", reg.DisposeSession(context.Background(), ctx.SessionID))
	})
	if start.ExitedEarly || captured.Process == nil || !captured.Process.Running || captured.Process.Handle != start.Handle {
		t.Fatalf("live launch status = %+v / %+v", start, captured.Process)
	}
	cancel()
	if !reg.HasRunning(ctx.SessionID) {
		t.Fatal("request cancellation stopped owned background work")
	}
	output := &command.CommandOutputTool{Registry: reg}
	stop := &command.CommandStopTool{Registry: reg}
	other := commandToolContext(ctx.Roots[0].Path, "other-session", "")
	for _, handler := range []interface {
		Run(context.Context, map[string]any, tools.ToolContext) (string, error)
	}{output, stop} {
		_, err := handler.Run(t.Context(), map[string]any{"handle": start.Handle}, other)
		var rejected *tools.ToolReject
		if !errors.As(err, &rejected) || rejected.Code != "COMMAND_OUTPUT_NO_LIVE_JOB" {
			t.Fatalf("cross-session handle error = %v", err)
		}
	}
	if !reg.HasRunning(ctx.SessionID) {
		t.Fatal("foreign stop changed owning session")
	}
	raw, err = output.Run(t.Context(), map[string]any{"handle": start.Handle}, ctx)
	testutil.FailErr(t, "read live output", err)
	var first struct {
		NextCursor string `json:"next_cursor"`
		Running    bool   `json:"running"`
		Chunks     []struct {
			Text string `json:"text"`
		} `json:"chunks"`
	}
	testutil.FailErr(t, "decode live output", json.Unmarshal([]byte(raw), &first))
	var text strings.Builder
	for _, chunk := range first.Chunks {
		text.WriteString(chunk.Text)
	}
	if !first.Running || text.String() != "first-output" {
		t.Fatalf("first output = %s", raw)
	}
	cursor, err := strconv.ParseInt(first.NextCursor, 10, 64)
	testutil.FailErr(t, "parse output cursor", err)
	raw, err = output.Run(t.Context(), map[string]any{"handle": start.Handle, "cursor": float64(cursor)}, ctx)
	testutil.FailErr(t, "read output after cursor", err)
	if strings.Contains(raw, "first-output") {
		t.Fatalf("cursor replayed prior output: %s", raw)
	}
	_, err = stop.Run(t.Context(), map[string]any{"handle": start.Handle}, ctx)
	testutil.FailErr(t, "stop owned background command", err)
	settled, err := reg.Await(t.Context(), ctx.SessionID, start.Handle, 5*time.Second)
	testutil.FailErr(t, "await stopped command", err)
	if !settled || reg.HasRunning(ctx.SessionID) {
		t.Fatal("stop did not settle background job")
	}
	raw, err = output.Run(t.Context(), map[string]any{"handle": start.Handle}, ctx)
	testutil.FailErr(t, "read output after stop", err)
	if !strings.Contains(raw, "first-output") || !strings.Contains(raw, `"running":false`) {
		t.Fatalf("retained stopped output = %s", raw)
	}
}
