package terminal

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/ptyinput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
)

type terminalSendArgs struct {
	ID      string
	Input   string
	Observe terminalObserveMode
}

// SendResult omits byte counts because resolved input lengths can disclose protected material.
type SendResult struct {
	ID       string                  `json:"id"`
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

// SendHandler builds the terminal_send handler.
func SendHandler(bg *bgprocess.Registry) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		in, err := parseTerminalSendArgs(args)
		if err != nil {
			return "", err
		}
		if err := requireTerminalRunning(bg, tctx.SessionID, in.ID); err != nil {
			return "", err
		}
		subject, _ := bg.CommandLine(tctx.SessionID, in.ID)
		tctx.SetDisplaySubject(subject)
		payload, err := ptyinput.ExpandControlInput(in.Input)
		if err != nil {
			return "", &tools.ToolReject{
				Code: "TERMINAL_CONTROL_UNKNOWN",
				Data: map[string]any{"reason": err.Error()},
			}
		}
		if err := tctx.Secrets.HandOff(ctx, nil); err != nil {
			return "", tools.HeldHandOffReject("terminal_send", err)
		}
		if err := bg.WritePTY(tctx.SessionID, in.ID, payload); err != nil {
			return "", mapTerminalLifecycleReject(err, in.ID)
		}
		running := bg.RequireRunning(tctx.SessionID, in.ID) == nil
		outPayload := SendResult{ID: in.ID, Running: running, Observe: in.Observe}
		if report, reportErr := bg.PTYReport(tctx.SessionID, in.ID); reportErr == nil {
			outPayload.Report = report
		}
		switch in.Observe {
		case terminalObserveAck:
		case terminalObserveDelta:
			delta, err := readTerminal(bg, tctx, SendToolName, in.ID, 0, 0, defaultTerminalReadBytes, true)
			if err != nil {
				return "", err
			}
			outPayload.Delta = &delta
			outPayload.Report = delta.Report
			outPayload.Running = delta.Running
		case terminalObserveScreen:
			snap, err := captureTerminalSnapshot(ctx, bg, tctx, SendToolName, in.ID, 0, 0, "", false)
			if err != nil {
				return "", err
			}
			attachSnapshotToSend(&outPayload, snap)
		}
		out, _ := surveyjson.Marshal(outPayload)
		return string(out), nil
	}
}

func attachSnapshotToSend(out *SendResult, snap SnapshotResult) {
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

func parseTerminalSendArgs(args map[string]any) (terminalSendArgs, error) {
	id, _ := args["id"].(string)
	id = strings.TrimSpace(id)
	if id == "" {
		return terminalSendArgs{}, tools.RejectInvalidArguments("TERMINAL_ID_REQUIRED", map[string]any{"reason": "missing_id"})
	}
	input, _ := args["input"].(string)
	if input == "" {
		return terminalSendArgs{}, tools.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "missing_input"})
	}
	observe, err := parseTerminalObserve(args)
	if err != nil {
		return terminalSendArgs{}, err
	}
	out := terminalSendArgs{ID: id, Input: input, Observe: observe}
	return out, nil
}
