package command

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/toolrejection"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// CommandOutputTool returns buffered stdout/stderr for a background process handle.
type CommandOutputTool struct {
	Registry *bgprocess.Registry
}

func (t *CommandOutputTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	if t.Registry == nil {
		return "", fmt.Errorf("background registry not configured")
	}
	handle, _ := args["handle"].(string)
	if handle == "" {
		return "", &toolrejection.ToolReject{Code: "BACKGROUND_HANDLE_REQUIRED", Data: map[string]any{}}
	}
	subject, _ := t.Registry.CommandLine(tctx.Identity.SessionID, handle)
	tctx.SetDisplaySubject(subject)
	cursor := int64(0)
	if v, ok := args["cursor"].(float64); ok {
		cursor = int64(v)
	}
	snapshot, err := t.Registry.Output.ReadCommandOutput(ctx, tctx.Identity.SessionID, handle, cursor)
	if err != nil {
		if errors.Is(err, bgprocess.ErrProcessNotFound) {
			return "", missingCommandHandleReject(t.Registry, tctx.Identity.SessionID, handle)
		}
		return "", err
	}
	t.Registry.Observe(tctx.Identity.SessionID, handle)
	out := commandOutputOf(snapshot.Output, snapshot.TerminationReason)
	capBackgroundOutput(&out, 0)
	payload := observeBackgroundOutput(tctx, out, snapshot.Confinement, snapshot.TerminationReason)
	payload.ExecFailure = snapshot.Failure
	t.Registry.Output.NoteRefusalsShown(tctx.Identity.SessionID, handle, len(payload.SandboxRefusals))
	encoded, err := surveyjson.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("command_output encode: %w", err)
	}
	return string(encoded), nil
}

// commandOutput is command_output's view of retained output, in the tool's
// handle and cursor vocabulary.
type commandOutput struct {
	Handle            string                      `json:"handle"`
	Cursor            string                      `json:"cursor"`
	NextCursor        string                      `json:"next_cursor"`
	Chunks            []bgprocess.OutputChunk     `json:"chunks"`
	Running           bool                        `json:"running"`
	AwaitingApproval  bool                        `json:"awaiting_approval,omitempty"`
	Truncated         bool                        `json:"truncated,omitempty"`
	ExitCode          *int                        `json:"exit_code,omitempty"`
	TerminationReason bgprocess.TerminationReason `json:"termination_reason,omitempty"`
}

func commandOutputOf(raw bgprocess.RawOutput, reason bgprocess.TerminationReason) commandOutput {
	return commandOutput{
		Handle:            raw.Handle,
		Cursor:            strconv.FormatInt(raw.From, 10),
		NextCursor:        strconv.FormatInt(raw.Next, 10),
		Chunks:            raw.Chunks,
		Running:           raw.Running,
		AwaitingApproval:  raw.AwaitingPerson,
		Truncated:         raw.Truncated,
		ExitCode:          raw.ExitCode,
		TerminationReason: reason,
	}
}

type commandOutputResult struct {
	commandOutput
	// ExecFailure is the run error the exit status does not carry.
	ExecFailure *hostcmd.ExecFailure `json:"exec_failure,omitempty"`
	Network     []confine.EgressHost `json:"network,omitempty"`
	confine.Report
}

func observeBackgroundOutput(
	tctx tools.ToolContext,
	out commandOutput,
	confinement bgprocess.JobConfinement,
	terminationReason bgprocess.TerminationReason,
) commandOutputResult {
	report := confinement.Report
	network := confinement.Network()
	refusals := confinement.Refusals()
	if confinement.Boundary.Applied {
		stamped := confine.StampRefusal(
			"command_output", tctx.Identity.SessionID, confinement.Boundary,
			confine.RefusalContext{
				MediatedNetwork:        network,
				RemotePackageExecution: report.RemotePackageExecution,
				Running:                out.Running,
				TerminationReason:      string(terminationReason),
				Refusals:               refusals,
			},
		)
		report.BoundaryRefusal = string(stamped.Attribution)
		if tctx.Effects.Out != nil {
			tctx.Effects.Out.Facts = tools.ApplyRefusalFacts(tctx.Effects.Out.Facts, stamped)
		}
	}
	return commandOutputResult{commandOutput: out, Network: network, Report: report.WithSandboxRefusals(refusals)}
}

type CommandStopTool struct {
	Registry *bgprocess.Registry
}

func (t *CommandStopTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	if t.Registry == nil {
		return "", fmt.Errorf("background registry not configured")
	}
	handle, _ := args["handle"].(string)
	if handle == "" {
		return "", &toolrejection.ToolReject{Code: "BACKGROUND_HANDLE_REQUIRED", Data: map[string]any{}}
	}
	subject, _ := t.Registry.CommandLine(tctx.Identity.SessionID, handle)
	tctx.SetDisplaySubject(subject)
	out, err := t.Registry.Lifecycle.Stop(tctx.Identity.SessionID, handle, bgprocess.TerminationStoppedByAgent)
	if err != nil {
		if errors.Is(err, bgprocess.ErrProcessNotFound) {
			return "", missingCommandHandleReject(t.Registry, tctx.Identity.SessionID, handle)
		}
		return "", err
	}
	encoded, err := surveyjson.Marshal(toolkit.ProcessStopResult{
		Handle: handle, StopRequested: out.StopRequested, Running: out.Running, ExitCode: out.ExitCode,
		TerminationReason: string(out.TerminationReason),
	})
	if err != nil {
		return "", fmt.Errorf("command_stop encode: %w", err)
	}
	return string(encoded), nil
}

// missingCommandHandleReject picks COMMAND_OUTPUT_NO_LIVE_JOB when nothing is running.
func missingCommandHandleReject(reg *bgprocess.Registry, sessionID, handle string) error {
	data := map[string]any{"handle": handle}
	if reg == nil || !reg.HasRunning(sessionID) {
		return &toolrejection.ToolReject{Code: "COMMAND_OUTPUT_NO_LIVE_JOB", Data: data}
	}
	return &toolrejection.ToolReject{Code: "BACKGROUND_HANDLE_NOT_FOUND", Data: data}
}

// backgroundLaunchGrace bounds inline exit detection.
const backgroundLaunchGrace = 400 * time.Millisecond

func commandStartError(err error) error {
	if err == nil {
		return nil
	}
	if tr := rejectStartCommandNotFound(err); tr != nil {
		return tr
	}
	if tr := rejectStartCommandExecDenied(err); tr != nil {
		return tr
	}
	var backgroundCapacity *bgprocess.BackgroundCapacityError
	if errors.As(err, &backgroundCapacity) {
		return &toolrejection.ToolReject{
			Code: "BACKGROUND_CAP_REACHED",
			Data: map[string]any{"background_limit": backgroundCapacity.Limit, "live_terminal_ids": backgroundCapacity.TerminalIDs, "live_command_handles": backgroundCapacity.CommandHandles},
		}
	}
	var capacity *bgprocess.AwaitedCapacityError
	if errors.As(err, &capacity) {
		return &toolrejection.ToolReject{Code: "COMMAND_CONCURRENCY_CAP_REACHED", Data: map[string]any{
			"max_awaited": capacity.Limit, "count": len(capacity.Handles),
			"live_command_handles": capacity.Handles,
		}}
	}
	var conflict *bgprocess.StartConflict
	if !errors.As(err, &conflict) {
		return err
	}
	code := "COMMAND_IN_FLIGHT"
	if errors.Is(conflict, bgprocess.ErrDuplicateRunning) {
		code = "COMMAND_DUPLICATE_RUNNING"
	}
	return &toolrejection.ToolReject{Code: code, Data: map[string]any{
		"handles": conflict.Handles, "handles_text": strings.Join(conflict.Handles, ", "),
	}}
}

// capBackgroundOutput caps aggregate command_output chunk text at the spill-file ceiling.
func capBackgroundOutput(out *commandOutput, maxBytes int) {
	if out == nil || len(out.Chunks) == 0 {
		return
	}
	var b strings.Builder
	for _, c := range out.Chunks {
		b.WriteString(c.Text)
	}
	joined := b.String()
	capped, truncated, _ := capOpaqueTail(joined, maxBytes)
	if !truncated {
		return
	}
	out.Truncated = true
	stream := out.Chunks[0].Stream
	if stream == "" {
		stream = "stdout"
	}
	out.Chunks = []bgprocess.OutputChunk{
		{Cursor: out.Chunks[0].Cursor, Stream: stream, Text: capped},
	}
}
