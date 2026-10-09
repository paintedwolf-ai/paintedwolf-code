package native

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
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
	snapshot, err := t.Registry.ReadRawOutput(tctx.Identity.SessionID, handle, cursor)
	if err != nil {
		if errors.Is(err, bgprocess.ErrProcessNotFound) {
			return "", missingCommandHandleReject(t.Registry, tctx.Identity.SessionID, handle)
		}
		return "", err
	}
	out := commandOutputOf(snapshot.Output)
	capBackgroundOutput(&out, 0)
	payload := observeBackgroundOutput(tctx, out, snapshot.Boundary, snapshot.Facts)
	payload.ExecFailure = snapshot.Failure
	t.Registry.NoteRefusalsShown(tctx.Identity.SessionID, handle, len(payload.SandboxRefusals))
	encoded, err := surveyjson.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("command_output encode: %w", err)
	}
	return string(encoded), nil
}

// commandOutput is command_output's view of retained output, in the tool's
// handle and cursor vocabulary.
type commandOutput struct {
	Handle     string                  `json:"handle"`
	Cursor     string                  `json:"cursor"`
	NextCursor string                  `json:"next_cursor"`
	Chunks     []bgprocess.OutputChunk `json:"chunks"`
	Running    bool                    `json:"running"`
	Truncated  bool                    `json:"truncated,omitempty"`
	ExitCode   *int                    `json:"exit_code,omitempty"`
}

func commandOutputOf(raw bgprocess.RawOutput) commandOutput {
	return commandOutput{
		Handle:     raw.Handle,
		Cursor:     strconv.FormatInt(raw.From, 10),
		NextCursor: strconv.FormatInt(raw.Next, 10),
		Chunks:     raw.Chunks,
		Running:    raw.Running,
		Truncated:  raw.Truncated,
		ExitCode:   raw.ExitCode,
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
	boundary confine.Boundary,
	facts confine.SpawnFacts,
) commandOutputResult {
	report := facts.Report
	network := facts.MediatedNetwork()
	refusals := facts.Refusals()
	if boundary.Applied {
		stamped := confine.StampRefusal(
			"command_output", tctx.Identity.SessionID, boundary,
			confine.RefusalContext{
				MediatedNetwork:        network,
				RemotePackageExecution: report.RemotePackageExecution,
				Running:                out.Running,
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
	out, err := t.Registry.Stop(tctx.Identity.SessionID, handle)
	if err != nil {
		if errors.Is(err, bgprocess.ErrProcessNotFound) {
			return "", missingCommandHandleReject(t.Registry, tctx.Identity.SessionID, handle)
		}
		return "", err
	}
	encoded, err := surveyjson.Marshal(toolkit.ProcessStopResult{
		Handle: handle, StopRequested: out.StopRequested, Running: out.Running, ExitCode: out.ExitCode,
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

// rejectBackgroundCapture refuses capture modes that need a foreground run.
func rejectBackgroundCapture(args map[string]any) error {
	for _, capture := range []string{"terminal_capture", "snapshot_capture"} {
		if _, ok := args[capture]; ok {
			return toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": capture + "_incompatible", "field": "background"})
		}
	}
	return nil
}

func runCommandBackground(
	ctx context.Context,
	registry *bgprocess.Registry,
	runner *hostcmd.Runner,
	boundary *sandbox.Boundary,
	args map[string]any,
	tctx tools.ToolContext,
	extraWriteRoots []string,
	toolName string,
) (string, error) {
	if registry == nil {
		return "", fmt.Errorf("background registry not configured")
	}
	if runner == nil {
		return "", fmt.Errorf("command runner not configured")
	}
	if reject := rejectBackgroundCapture(args); reject != nil {
		return "", reject
	}
	plan, err := tctx.CommandPlan(args)
	if err != nil {
		return "", err
	}
	stages := plan.Stages
	ioParams, err := CommandIO(ctx, boundary, tctx, plan, args, toolName)
	if err != nil {
		return "", err
	}
	cwdArg, _ := args["cwd"].(string)
	cwd, cwdDisplay, err := projectpaths.CommandCwd(ctx, tctx, cwdArg)
	if err != nil {
		return "", err
	}
	if err := tools.ValidateWorkerBranch(ctx, tctx); err != nil {
		return "", err
	}
	if tr := rejectPwdEnvMismatch(args, canonicalCommandKey(tctx, args), cwd); tr != nil {
		return "", tr
	}
	confReq, tr := tools.ConfineRequestForSpawn(ctx, tctx, extraWriteRoots)
	if tr != nil {
		return "", tr
	}
	confinement, applied := confine.DefaultConfinement(confReq)
	if err := confine.RequireApplied(applied); err != nil {
		return "", err
	}
	confine.LogApplied("background", tctx.Identity.SessionID, confinement)
	directIPApplied := tctx.Direct.DirectIPRequested && confinement != nil && confinement.Network == confine.NetworkDirectIP
	commandLine := canonicalCommandKey(tctx, args)
	egressLease, err := confine.BindAction(confinement, commandEgressIdentity(tctx, toolName, commandLine))
	if err != nil {
		return "", err
	}
	profile := tctx.ProfileID()
	req := hostcmd.Request{
		Launch:     agentCommandLaunch(tctx, toolName, confinement),
		ProjectDir: cwd,
		ProfileID:  profile,
		Stages:     stages,
		IOParams:   ioParams,
		PathExtra:  append([]string(nil), tctx.Host.HostResourcePathExtra...),
	}
	spawnFacts := confine.SpawnFacts{Report: commandConfinementReport(
		confine.BoundaryOf(confinement), commandNetworkPosture(tctx, toolName, commandLine),
		tools.LocalNetworkGrantOf(tctx), tctx.Files.PackageExecution,
	), Action: egressLease, Network: egressLease.ObservedHosts}
	if err := tctx.Effects.Secrets.HandOff(ctx, nil); err != nil {
		return "", toolrejection.HeldHandOffReject(toolName, err)
	}
	window := openCommandWindow(ctx, tctx, toolName, commandLine)
	var sourceRevision, sourceRootDigest string
	if tctx.Execution.VerificationCheck {
		sourceRevision, sourceRootDigest = sourceledger.VerificationState(ctx, tctx.Source.Observations, tools.HostWriteRoot(tctx))
	}
	index := watchIndex(tctx, confinement)
	handle, err := registry.StartPipeline(ctx, bgprocess.PipelineSpec{
		IsCheck:        tctx.Execution.VerificationCheck,
		SourceRevision: sourceRevision, SourceRootDigest: sourceRootDigest, Cwd: cwdDisplay,
		SessionID: tctx.Identity.SessionID, RootSessionID: tctx.ChatSessionID(),
		ProjectID: tctx.Identity.ProjectID, Request: req,
		Runner: runner,
		Mode:   bgprocess.JobModeBackground, OriginTool: toolName,
		ToolCallID: tctx.Identity.ToolCallID, RunID: commandRunID(tctx),
		Timeout: commandTimeout(args, toolName), AllowConcurrent: true,
		Facts: spawnFacts,
	})
	if err != nil {
		index.Release()
		window.closeNow(ctx)
		if egressLease != nil {
			egressLease.Close(ctx)
		}
		return "", commandStartError(err)
	}
	if directIPApplied {
		tools.EmitDirectIPLifecycle(tctx, tools.DirectIPLifecycleStarted)
	}
	networkLife := newCommandNetworkLifecycle(tctx, toolName, egressLease, directIPApplied)
	registry.WatchIndex(tctx.Identity.SessionID, handle, index)
	// The exit hook fires after this call returns and its ctx is canceled.
	exitCtx := context.WithoutCancel(ctx)
	registry.OnExit(tctx.Identity.SessionID, handle, func() {
		networkLife.complete(exitCtx)
		if snap, snapErr := registry.Snapshot(exitCtx, tctx.Identity.SessionID, handle, bgprocess.DefaultTailBytes); snapErr == nil {
			recordContainerLaunch(tctx, snap)
		}
	})
	window.closeOnExit(ctx, registry, tctx.Identity.SessionID, handle)
	result := hostcmd.BackgroundStartResult{
		Background: true,
		Handle:     handle,
		Stages:     hostcmd.StagePlaceholders(stages),
	}
	var observedNetwork []confine.EgressHost
	var boundaryRefusal confine.FailureAttribution
	refusals := egressLease.Refusals()
	// Report fast exits inline instead of as running handles.
	if settled, code, tail, exited := awaitBackgroundEarlyExit(ctx, registry, tctx.Identity.SessionID, handle); exited {
		observedNetwork = networkLife.complete(ctx)
		refusals = egressLease.SettledRefusals(ctx)
		if len(settled) > 0 {
			result.Stages = settled
		}
		result.ExitedEarly = true
		result.ExitCode = &code
		if snap, snapErr := registry.Snapshot(ctx, tctx.Identity.SessionID, handle, bgprocess.DefaultTailBytes); snapErr == nil {
			recordContainerLaunch(tctx, snap)
		} else {
			recordContainerLaunch(tctx, bgprocess.Snapshot{Tail: tail, Output: tail})
		}
		if confinement != nil {
			stamped := confine.StampRefusal(toolName, tctx.Identity.SessionID,
				confine.BoundaryOf(confinement),
				confine.RefusalContext{
					MediatedNetwork:        observedNetwork,
					RemotePackageExecution: spawnFacts.Report.RemotePackageExecution,
					FailedStages:           hostcmd.FailedStages(settled, code),
					Refusals:               refusals,
				})
			boundaryRefusal = stamped.Attribution
			if tctx.Effects.Out != nil {
				tctx.Effects.Out.Facts = tools.ApplyRefusalFacts(tctx.Effects.Out.Facts, stamped)
			}
		}
		result.Tail = tail
	}
	result.Network = observedNetwork
	result.Report = commandConfinementReport(confine.BoundaryOf(confinement),
		commandNetworkPosture(tctx, toolName, commandLine), tools.LocalNetworkGrantOf(tctx), tctx.Files.PackageExecution,
	).WithSandboxRefusals(refusals)
	result.BoundaryRefusal = string(boundaryRefusal)
	if !result.ExitedEarly {
		registry.NoteRefusalsShown(tctx.Identity.SessionID, handle, len(refusals.Refusals))
	}
	tools.CaptureExternalAccess(tctx, observedNetwork, directIPApplied)
	// Only live handles keep the status indicator running.
	captureProcessHandle(tctx, handle, !result.ExitedEarly)
	out, err := surveyjson.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("command background encode: %w", err)
	}
	return string(out), nil
}

// backgroundLaunchGrace bounds inline exit detection.
const backgroundLaunchGrace = 400 * time.Millisecond

// awaitBackgroundEarlyExit reports jobs that exit during launch grace.
// Settled stages preserve sequence skips.
func awaitBackgroundEarlyExit(
	ctx context.Context, registry *bgprocess.Registry, sessionID, handle string,
) (stages []hostcmd.StageResult, exitCode int, tail string, exited bool) {
	deadline := time.Now().Add(backgroundLaunchGrace)
	for {
		snapshot, err := registry.ReadRawOutput(sessionID, handle, 0)
		if err != nil {
			return nil, 0, "", false
		}
		out := snapshot.Output
		if !out.Running && out.ExitCode != nil {
			var text strings.Builder
			for _, chunk := range out.Chunks {
				text.WriteString(chunk.Text)
			}
			// Exit and stage outcomes settle together.
			var settled []hostcmd.StageResult
			if snap, snapErr := registry.Snapshot(ctx, sessionID, handle, 0); snapErr == nil {
				settled = snap.Stages
			}
			return settled, *out.ExitCode, text.String(), true
		}
		if time.Now().After(deadline) {
			return nil, 0, "", false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

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
