package terminal

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
	"github.com/lycaon/lycaon/pkg/api"
)

type terminalSnapshotArgs struct {
	ID        string `json:"id"`
	IdleMS    int    `json:"idle_ms"`
	TimeoutMS int    `json:"timeout_ms"`
	Caption   string `json:"caption"`
}

type terminalSnapshotState struct {
	Cols      int  `json:"cols"`
	Rows      int  `json:"rows"`
	CursorCol int  `json:"cursor_col"`
	CursorRow int  `json:"cursor_row"`
	Busy      bool `json:"busy"`
	AltScreen bool `json:"alt_screen"`
	Running   bool `json:"running"`
}

// SnapshotResult is the terminal_snapshot tool payload.
type SnapshotResult struct {
	ID       string                 `json:"id"`
	Observe  terminalObserveMode    `json:"observe"`
	Surface  string                 `json:"surface"`
	State    terminalSnapshotState  `json:"state"`
	Snapshot terminalSnapshotGrid   `json:"snapshot"`
	Coverage terminalScreenCoverage `json:"coverage"`
	Caption  string                 `json:"caption,omitempty"`
	Mime     string                 `json:"mime,omitempty"`
	Width    int                    `json:"width,omitempty"`
	Height   int                    `json:"height,omitempty"`
	// CaptureFromScreen inherits the enclosing command report.
	*confine.Report
	Receipt surveyreceipt.Receipt `json:"receipt"`
}

// CaptureFromScreen builds terminal evidence for a sealed one-shot command.
// The empty id is intentional: no mutable terminal handle escapes the command.
func CaptureFromScreen(
	ctx context.Context, bg *bgprocess.Registry, tctx tools.ToolContext,
	screen bgprocess.ScreenSnapshot, caption string,
) SnapshotResult {
	if safe, err := bg.ProjectCapturedText(
		ctx, tctx.Identity.ProjectID, tctx.Identity.ParentSessionID, tctx.Identity.SessionID,
		"capture.terminal.caption", caption,
	); err == nil {
		caption = safe
	} else {
		caption = "Screened terminal capture"
	}
	payload := SnapshotResult{
		Observe: terminalObserveScreen,
		Surface: "tui",
		State: terminalSnapshotState{
			Cols: screen.Cols, Rows: screen.Rows, CursorCol: screen.CursorCol,
			CursorRow: screen.CursorRow, AltScreen: screen.AltScreen, Running: false,
		},
		Caption: caption,
	}
	payload.Snapshot, payload.Coverage = boundedTerminalGrid(screen)
	if mime, png, width, height, err := renderTerminalGridPNG(screen); err == nil && len(png) > 0 {
		payload.Mime, payload.Width, payload.Height = mime, width, height
		if tctx.Effects.Out != nil {
			tctx.Effects.Out.Visual = &tools.VisualCapture{
				Mime: mime, Bytes: png, Source: api.VisualArtifactSourceCapture,
				Caption: caption, Perceive: true, Projected: true,
			}
		}
	}
	payload.Receipt = terminalReceipt("command", tctx, "sealed", int64(screen.CursorRow), terminalSnapshotBytes(payload))
	payload.Receipt.Truncated = payload.Coverage.Truncated
	return payload
}

// SnapshotHandler builds the terminal_snapshot handler.
func SnapshotHandler(bg *bgprocess.Registry) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		in, err := parseTerminalSnapshotArgs(args)
		if err != nil {
			return "", err
		}
		if err := bg.LookupPTY(tctx.Identity.SessionID, in.ID); err != nil {
			return "", mapTerminalLifecycleReject(err, in.ID)
		}
		subject, _ := bg.CommandLine(tctx.Identity.SessionID, in.ID)
		tctx.SetDisplaySubject(subject)
		payload, err := captureTerminalSnapshot(ctx, bg, tctx, SnapshotToolName, in.ID, in.IdleMS, in.TimeoutMS, in.Caption, false)
		if err != nil {
			return "", err
		}
		out, _ := surveyjson.Marshal(payload)
		return string(out), nil
	}
}

func captureTerminalSnapshot(
	ctx context.Context,
	bg *bgprocess.Registry,
	tctx tools.ToolContext,
	tool string,
	id string,
	idleMS, timeoutMS int,
	caption string,
	waitForOutput bool,
) (SnapshotResult, error) {
	opts := bgprocess.PTYReadOpts{WaitForOutput: waitForOutput}
	if idleMS > 0 {
		opts.Idle = time.Duration(idleMS) * time.Millisecond
	}
	if timeoutMS > 0 {
		opts.Timeout = time.Duration(timeoutMS) * time.Millisecond
	}
	res, err := bg.SnapshotPTY(ctx, tctx.Identity.SessionID, id, opts)
	if err != nil {
		return SnapshotResult{}, mapTerminalLifecycleReject(err, id)
	}
	caption, err = bg.ProjectCapturedText(
		ctx, tctx.Identity.ProjectID, tctx.Identity.ParentSessionID, tctx.Identity.SessionID,
		"capture.terminal.caption", caption,
	)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("project terminal caption: %w", err)
	}
	report := stampBoundary(tctx, tool, snapshotObservation(res))
	payload := SnapshotResult{
		ID:      id,
		Observe: terminalObserveScreen,
		Surface: "tui",
		Report:  &report,
		State: terminalSnapshotState{
			Cols:      res.Screen.Cols,
			Rows:      res.Screen.Rows,
			CursorCol: res.Screen.CursorCol,
			CursorRow: res.Screen.CursorRow,
			Busy:      res.Running,
			AltScreen: res.Screen.AltScreen,
			Running:   res.Running,
		},
		Caption: caption,
	}
	payload.Snapshot, payload.Coverage = boundedTerminalGrid(res.Screen)
	// VisualArtifact — degrade to text-only when render fails.
	if mime, png, w, h, rerr := renderTerminalGridPNG(res.Screen); rerr == nil && len(png) > 0 {
		payload.Mime = mime
		payload.Width = w
		payload.Height = h
		if tctx.Effects.Out != nil {
			tctx.Effects.Out.Visual = &tools.VisualCapture{
				Mime:      mime,
				Bytes:     png,
				Source:    api.VisualArtifactSourceCapture,
				Caption:   caption,
				Perceive:  true,
				Projected: true,
			}
		}
	}
	payload.Receipt = terminalReceipt(SnapshotToolName, tctx, id, int64(res.Screen.CursorRow), terminalSnapshotBytes(payload))
	payload.Receipt.Truncated = payload.Coverage.Truncated
	return payload, nil
}

func terminalSnapshotBytes(payload SnapshotResult) int {
	copy := payload
	copy.Receipt = surveyreceipt.Receipt{}
	encoded, _ := surveyjson.Marshal(copy)
	return len(encoded)
}

func parseTerminalSnapshotArgs(args map[string]any) (terminalSnapshotArgs, error) {
	id, _ := args["id"].(string)
	id = strings.TrimSpace(id)
	if id == "" {
		return terminalSnapshotArgs{}, toolrejection.RejectInvalidArguments("TERMINAL_ID_REQUIRED", map[string]any{"reason": "missing_id"})
	}
	out := terminalSnapshotArgs{ID: id}
	if v, ok := args["idle_ms"].(float64); ok && v > 0 {
		out.IdleMS = int(v)
	}
	if v, ok := args["timeout_ms"].(float64); ok && v > 0 {
		out.TimeoutMS = int(v)
	}
	if c, ok := args["caption"].(string); ok {
		out.Caption = strings.TrimSpace(c)
	}
	return out, nil
}
