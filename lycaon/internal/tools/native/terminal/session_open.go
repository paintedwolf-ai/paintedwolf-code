package terminal

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
)

type terminalOpenArgs struct {
	Command string
	Cwd     string
	WinSize lycexec.WinSize
	Env     map[string]string
	Observe terminalObserveMode
}

// OpenResult is the terminal_open payload with a reference-bearing command.
type OpenResult struct {
	ID       string                  `json:"id"`
	Command  string                  `json:"command,omitempty"`
	Running  bool                    `json:"running"`
	Observe  terminalObserveMode     `json:"observe"`
	Surface  string                  `json:"surface,omitempty"`
	State    *terminalSnapshotState  `json:"state,omitempty"`
	Snapshot *terminalSnapshotGrid   `json:"snapshot,omitempty"`
	Coverage *terminalScreenCoverage `json:"coverage,omitempty"`
	Delta    *TerminalReadResult     `json:"delta,omitempty"`
	Caption  string                  `json:"caption,omitempty"`
	Mime     string                  `json:"mime,omitempty"`
	Width    int                     `json:"width,omitempty"`
	Height   int                     `json:"height,omitempty"`
	confine.Report
	Receipt *surveyreceipt.Receipt `json:"receipt,omitempty"`
}

// OpenHandler builds the terminal_open handler.
func OpenHandler(bg *bgprocess.Registry) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		in, err := parseTerminalOpenArgs(args)
		if err != nil {
			return "", err
		}
		if _, hasPipeline := args["pipeline"]; hasPipeline {
			return "", tools.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "pipeline_not_supported", "field": "pipeline"})
		}
		plan, err := tctx.CommandPlan(map[string]any{"command": in.Command})
		if err != nil {
			if errors.Is(err, argv.ErrShellMetacharacters) || errors.Is(err, argv.ErrUnterminatedQuote) ||
				errors.Is(err, argv.ErrRedirectionUnsupported) {
				return "", err
			}
			return "", tools.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": err.Error()})
		}
		stages := plan.Stages
		if len(stages) != 1 {
			// A PTY runs one process; sequences require the command surface.
			return "", commandsurface.ErrSequenceUnsupported
		}
		if !stages[0].Streams.Terminal() {
			// The terminal owns every stream; a file redirection has nowhere to attach.
			return "", &argv.RedirectionError{Issue: argv.IssueSurfaceUnsupported, Operator: argv.RenderRedirects(stages[0].Redirects)}
		}
		cwd, _, err := projectpaths.CommandCwd(ctx, tctx, in.Cwd)
		if err != nil {
			return "", err
		}
		if cwd == "" {
			return "", &tools.ToolReject{Code: "PROJECT_HAS_NO_ROOTS"}
		}
		if err := tools.ValidateWorkerBranch(ctx, tctx); err != nil {
			return "", err
		}
		// Terminal sessions take no chat write-root overlay.
		confReq, tr := tools.ConfineRequestForSpawn(ctx, tctx, nil)
		if tr != nil {
			return "", tr
		}
		confinement, applied := confine.DefaultConfinement(confReq)
		if err := confine.RequireApplied(applied); err != nil {
			return "", err
		}
		confine.LogApplied(OpenToolName, tctx.SessionID, confinement)
		egress := confine.EgressCommand{
			SessionID:     tctx.SessionID,
			RootSessionID: tctx.ChatSessionID(),
			ProjectID:     tctx.ProjectID,
			ProjectDir:    tctx.ActiveRootPath(),
			ToolCallID:    tctx.ToolCallID,
			ToolName:      OpenToolName,
			CommandLine:   in.Command,
		}
		if tctx.PackageExecution != nil {
			egress.DeclaredHosts = append([]string(nil), tctx.PackageExecution.AllowedHosts...)
			egress.ReducedPackageExecution = true
		}
		// A terminal session outlives its call, so the lease keeps its
		// descendants attributable until it closes.
		egressLease, err := confine.BindAction(confinement, egress)
		if err != nil {
			return "", err
		}
		// Every observation reuses this spawn report.
		report := confine.ReportOf(confine.BoundaryOf(confinement)).
			WithLocalNetwork(tools.LocalNetworkGrantOf(tctx))
		if tctx.PackageExecution != nil {
			report = report.WithRemotePackageExecution(tctx.PackageExecution.AllowedHosts, tctx.PackageExecution.ApprovedReadPaths)
		}
		if confinement != nil {
			report.NetworkPosture = confine.PostureString(confine.EffectivePosture(egress))
		}
		profile := tctx.ProfileID()
		hostRunner := hostcmd.NewRunner()
		launch := lycexec.AgentLaunch(lycexec.LaunchAgentCommand, "terminal_open", confinement)
		if tctx.PackageExecution != nil {
			launch = launch.WithReducedEnvironment()
		}
		req := hostcmd.Request{
			Launch:     launch,
			ProjectDir: cwd,
			ProfileID:  profile,
			Stages:     stages,
			IOParams:   hostcmd.IOParams{InlineEnv: in.Env},
		}
		facts := confine.SpawnFacts{Report: report, Network: egressLease.ObservedHosts}
		tctx.Secrets.HandOff(ctx, nil)
		handle, err := bg.StartPTY(
			ctx, tctx.SessionID, tctx.ParentSessionID, tctx.ProjectID,
			req, hostRunner, in.WinSize, facts,
		)
		if err != nil {
			egressLease.Close(ctx)
			return "", mapTerminalLifecycleReject(err, "")
		}
		ledgerCtx := context.WithoutCancel(ctx)
		bg.OnExit(tctx.SessionID, handle, func() {
			hosts := egressLease.Close(ledgerCtx)
			tools.RecordMediatedEgress(ledgerCtx, tctx, OpenToolName, hosts)
		})
		outPayload := OpenResult{
			ID: handle, Command: canonicalCommandEcho(tctx), Running: true,
			Observe: in.Observe, Report: report,
		}
		switch in.Observe {
		case terminalObserveAck:
		case terminalObserveDelta:
			delta, err := readTerminal(bg, tctx, OpenToolName, handle, 0, 0, defaultTerminalReadBytes, true)
			if err != nil {
				return "", err
			}
			outPayload.Delta = &delta
			outPayload.Report = delta.Report
			outPayload.Running = delta.Running
		case terminalObserveScreen:
			snap, err := captureTerminalSnapshot(ctx, bg, tctx, OpenToolName, handle, 0, 0, "", true)
			if err != nil {
				return "", err
			}
			attachSnapshotToOpen(&outPayload, snap)
		}
		out, _ := surveyjson.Marshal(outPayload)
		return string(out), nil
	}
}

// canonicalCommandEcho returns the reference-bearing command line.
func canonicalCommandEcho(tctx tools.ToolContext) string {
	command, _ := tctx.CanonicalArgs["command"].(string)
	return strings.TrimSpace(command)
}

func attachSnapshotToOpen(out *OpenResult, snap SnapshotResult) {
	state := snap.State
	grid := snap.Snapshot
	out.Surface = snap.Surface
	out.State = &state
	out.Snapshot = &grid
	coverage := snap.Coverage
	out.Coverage = &coverage
	out.Caption = snap.Caption
	out.Mime = snap.Mime
	out.Width = snap.Width
	out.Height = snap.Height
	receipt := snap.Receipt
	out.Receipt = &receipt
	if snap.Report != nil {
		out.Report = *snap.Report
	}
	out.Running = snap.State.Running
}

func parseTerminalOpenArgs(args map[string]any) (terminalOpenArgs, error) {
	cmd, _ := args["command"].(string)
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return terminalOpenArgs{}, tools.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "missing_command"})
	}
	out := terminalOpenArgs{Command: cmd}
	if c, ok := args["cwd"].(string); ok {
		out.Cwd = strings.TrimSpace(c)
	}
	if raw, exists := args["winsize"]; exists {
		winSize, ok := raw.(map[string]any)
		if !ok {
			return terminalOpenArgs{}, tools.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "winsize_must_be_object"})
		}
		cols, colsOK := winSize["cols"].(float64)
		rows, rowsOK := winSize["rows"].(float64)
		if !colsOK || !rowsOK || cols != float64(int(cols)) || rows != float64(int(rows)) || cols < 1 || rows < 1 || cols > maxTerminalColumns || rows > maxTerminalRows {
			return terminalOpenArgs{}, tools.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "winsize_out_of_range"})
		}
		out.WinSize = lycexec.WinSize{Cols: uint16(cols), Rows: uint16(rows)}
	}
	if raw, ok := args["env"].(map[string]any); ok && len(raw) > 0 {
		env := make(map[string]string, len(raw))
		for k, v := range raw {
			s, ok := v.(string)
			if !ok {
				return terminalOpenArgs{}, tools.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "env_values_must_be_strings"})
			}
			env[k] = s
		}
		if err := lycexec.ValidateInlineEnv(env); err != nil {
			return terminalOpenArgs{}, err
		}
		out.Env = env
	}
	observe, err := parseTerminalObserve(args)
	if err != nil {
		return terminalOpenArgs{}, err
	}
	out.Observe = observe
	return out, nil
}
