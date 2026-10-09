package terminal

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
)

type terminalReadArgs struct {
	ID        string `json:"id"`
	IdleMS    int    `json:"idle_ms"`
	TimeoutMS int    `json:"timeout_ms"`
	MaxBytes  int    `json:"max_bytes"`
}

// TerminalReadResult is one lossless stateful terminal_read page.
type TerminalReadResult struct {
	ID                     string `json:"id"`
	Text                   string `json:"text"`
	BytesReturned          int    `json:"bytes_returned"`
	Truncated              bool   `json:"truncated"`
	PageContinuation       bool   `json:"page_continuation"`
	EvictedBytes           int64  `json:"evicted_bytes"`
	Running                bool   `json:"running"`
	ExitCode               *int   `json:"exit_code,omitempty"`
	StartCursor            int64  `json:"start_cursor"`
	NextCursor             int64  `json:"next_cursor"`
	AvailableThroughCursor int64  `json:"available_through_cursor"`
	TruncationBanner       string `json:"truncation_banner,omitempty"`
	confine.Report
	Receipt surveyreceipt.Receipt `json:"receipt"`
}

// ReadHandler builds the terminal_read handler.
func ReadHandler(bg *bgprocess.Registry) tools.ToolHandler {
	return func(_ context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		in, err := parseTerminalReadArgs(args)
		if err != nil {
			return "", err
		}
		if err := bg.LookupPTY(tctx.Identity.SessionID, in.ID); err != nil {
			return "", mapTerminalLifecycleReject(err, in.ID)
		}
		subject, _ := bg.CommandLine(tctx.Identity.SessionID, in.ID)
		tctx.SetDisplaySubject(subject)
		payload, err := readTerminal(bg, tctx, ReadToolName, in.ID, in.IdleMS, in.TimeoutMS, in.MaxBytes, false)
		if err != nil {
			return "", err
		}
		out, _ := surveyjson.Marshal(payload)
		return string(out), nil
	}
}

func readTerminal(bg *bgprocess.Registry, tctx tools.ToolContext, tool, id string, idleMS, timeoutMS, maxBytes int, waitForOutput bool) (TerminalReadResult, error) {
	opts := bgprocess.PTYReadOpts{MaxBytes: maxBytes, WaitForOutput: waitForOutput}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = defaultTerminalReadBytes
	}
	if idleMS > 0 {
		opts.Idle = time.Duration(idleMS) * time.Millisecond
	}
	if timeoutMS > 0 {
		opts.Timeout = time.Duration(timeoutMS) * time.Millisecond
	}
	res, err := bg.ReadPTY(tctx.Identity.SessionID, id, opts)
	if err != nil {
		return TerminalReadResult{}, mapTerminalLifecycleReject(err, id)
	}
	banner := terminalReadBanner(res)
	out := TerminalReadResult{
		ID:                     id,
		Text:                   res.Text,
		Report:                 stampBoundary(tctx, tool, readObservation(res)),
		BytesReturned:          res.BytesReturned,
		Truncated:              res.Truncated,
		PageContinuation:       res.PageContinuation,
		EvictedBytes:           res.EvictedBytes,
		Running:                res.Running,
		ExitCode:               res.ExitCode,
		StartCursor:            res.StartCursor,
		NextCursor:             res.NextCursor,
		AvailableThroughCursor: res.AvailableThroughCursor,
		TruncationBanner:       banner,
	}
	out.Receipt = terminalReceipt(ReadToolName, tctx, id, res.StartCursor, res.BytesReturned)
	out.Receipt.Truncated = out.Truncated
	return out, nil
}

func terminalReadBanner(res bgprocess.PTYReadResult) string {
	parts := make([]string, 0, 2)
	if res.EvictedBytes > 0 {
		parts = append(parts, "TERMINAL_OUTPUT_EVICTED")
	}
	if res.PageContinuation {
		parts = append(parts, "TERMINAL_OUTPUT_CONTINUATION")
	}
	return strings.Join(parts, "\n")
}

func parseTerminalReadArgs(args map[string]any) (terminalReadArgs, error) {
	id, _ := args["id"].(string)
	id = strings.TrimSpace(id)
	if id == "" {
		return terminalReadArgs{}, toolrejection.RejectInvalidArguments("TERMINAL_ID_REQUIRED", map[string]any{"reason": "missing_id"})
	}
	out := terminalReadArgs{ID: id}
	if v, ok := args["idle_ms"].(float64); ok && v > 0 {
		out.IdleMS = int(v)
	}
	if v, ok := args["timeout_ms"].(float64); ok && v > 0 {
		out.TimeoutMS = int(v)
	}
	if v, ok := args["max_bytes"].(float64); ok {
		out.MaxBytes = int(v)
		if out.MaxBytes < 1024 || out.MaxBytes > maxTerminalReadBytes {
			return terminalReadArgs{}, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "max_bytes_out_of_range"})
		}
	}
	return out, nil
}
