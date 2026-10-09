package terminal

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/bgprocess"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
)

// Tool names for the terminal session family.
const (
	SendToolName     = "terminal_send"
	ReadToolName     = "terminal_read"
	OpenToolName     = "terminal_open"
	SnapshotToolName = "terminal_snapshot"
	CloseToolName    = "terminal_close"
)

type terminalObserveMode string

const (
	terminalObserveAck       terminalObserveMode = "ack"
	terminalObserveDelta     terminalObserveMode = "delta"
	terminalObserveScreen    terminalObserveMode = "screen"
	defaultTerminalReadBytes                     = 32 << 10
	maxTerminalReadBytes                         = 64 << 10
	maxTerminalColumns                           = 240
	maxTerminalRows                              = 120
)

func terminalReceipt(tool string, tctx tools.ToolContext, id string, cursor int64, bytesReturned int) surveyreceipt.Receipt {
	r := surveyreceipt.New(tool, id, 1, bytesReturned, false)
	sum := sha256.Sum256([]byte(strings.Join([]string{tctx.SessionID, tctx.ProjectID, id, tool, strconv.FormatInt(cursor, 10)}, "\x00")))
	r.ScopeHash = hex.EncodeToString(sum[:16])
	return r
}

func parseTerminalObserve(args map[string]any) (terminalObserveMode, error) {
	raw, ok := args["observe"]
	if !ok {
		return terminalObserveScreen, nil
	}
	observe, ok := raw.(string)
	if !ok {
		return "", toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "observe_must_be_string"})
	}
	switch terminalObserveMode(strings.TrimSpace(observe)) {
	case terminalObserveAck, terminalObserveDelta, terminalObserveScreen:
		return terminalObserveMode(strings.TrimSpace(observe)), nil
	default:
		return "", toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "observe_invalid"})
	}
}

func requireTerminalRunning(bg *bgprocess.Registry, sessionID, id string) error {
	if err := bg.RequireRunning(sessionID, id); err != nil {
		return mapTerminalLifecycleReject(err, id)
	}
	if err := bg.LookupPTY(sessionID, id); err != nil {
		return mapTerminalLifecycleReject(err, id)
	}
	return nil
}

func mapTerminalLifecycleReject(err error, id string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, lycexec.ErrPTYUnsupported) {
		return &toolrejection.ToolReject{Code: "TERMINAL_UNSUPPORTED", Data: map[string]any{"reason": "pty_unsupported"}}
	}
	code, reason := "TERMINAL_NOT_FOUND", "not_found"
	switch {
	case errors.Is(err, bgprocess.ErrProcessNotRunning):
		code, reason = "TERMINAL_NOT_RUNNING", "not_running"
	case errors.Is(err, bgprocess.ErrProcessNotFound):
		// defaults
	case errors.Is(err, bgprocess.ErrBackgroundCapReached):
		code, reason = "TERMINAL_CAP_REACHED", "cap_reached"
	case errors.Is(err, bgprocess.ErrNotPTY):
		reason = "not_pty"
	default:
		return err
	}
	data := map[string]any{"reason": reason, "terminal_failure": reason, "terminal_not_pty": errors.Is(err, bgprocess.ErrNotPTY)}
	var capacity *bgprocess.BackgroundCapacityError
	if errors.As(err, &capacity) {
		data["background_limit"] = capacity.Limit
		data["live_terminal_ids"] = capacity.TerminalIDs
		data["live_command_handles"] = capacity.CommandHandles
	}
	if id != "" {
		data["id"] = id
	}
	return &toolrejection.ToolReject{Code: code, Data: data}
}
