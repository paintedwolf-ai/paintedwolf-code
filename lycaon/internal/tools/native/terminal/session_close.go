package terminal

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

type terminalCloseArgs struct {
	ID string `json:"id"`
}

type terminalCloseResult struct {
	ID       string `json:"id"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Closed   bool   `json:"closed"`
	confine.Report
}

// CloseHandler builds the terminal_close handler.
func CloseHandler(bg *bgprocess.Registry) tools.ToolHandler {
	return func(_ context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		in, err := parseTerminalCloseArgs(args)
		if err != nil {
			return "", err
		}
		if err := bg.LookupPTY(tctx.Identity.SessionID, in.ID); err != nil {
			return "", mapTerminalLifecycleReject(err, in.ID)
		}
		subject, _ := bg.CommandLine(tctx.Identity.SessionID, in.ID)
		tctx.SetDisplaySubject(subject)
		res, err := bg.ClosePTY(tctx.Identity.SessionID, in.ID)
		if err != nil {
			return "", mapTerminalLifecycleReject(err, in.ID)
		}
		out, _ := surveyjson.Marshal(terminalCloseResult{
			ID: in.ID, ExitCode: res.ExitCode, Closed: true,
			Report: stampBoundary(tctx, CloseToolName, closeObservation(res)),
		})
		return string(out), nil
	}
}

func parseTerminalCloseArgs(args map[string]any) (terminalCloseArgs, error) {
	id, _ := args["id"].(string)
	id = strings.TrimSpace(id)
	if id == "" {
		return terminalCloseArgs{}, toolrejection.RejectInvalidArguments("TERMINAL_ID_REQUIRED", map[string]any{"reason": "missing_id"})
	}
	return terminalCloseArgs{ID: id}, nil
}
