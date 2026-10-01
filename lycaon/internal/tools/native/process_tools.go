package native

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// ProcessTools is host-coupled; the service owns process identity and kernel operations.
type ProcessTools struct{ Service *hostprocess.Service }

func (t *ProcessTools) List(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
	if t.Service == nil || tc.ProcessReview == nil {
		return "", safecmd.Reject(isolation.CodeApprovalUnavailable, nil)
	}
	if err := tc.ProcessReview(ctx, "list", nil); err != nil {
		return "", err
	}
	pid := toolkit.ClampIntArg(args, "pid", 0, 0, 1<<30)
	after := toolkit.ClampIntArg(args, "after_pid", 0, 0, 1<<30)
	limit := toolkit.ClampIntArg(args, "limit", 100, 1, 500)
	snapshot, err := t.Service.List(ctx, processTaskSession(tc), pid, after, limit)
	if err != nil {
		return "", processError(err)
	}
	raw, err := surveyjson.Marshal(map[string]any{"processes": snapshot.Processes, "next_after_pid": snapshot.NextAfterPID, "unavailable": snapshot.Unavailable, "snapshot_complete": snapshot.NextAfterPID == 0 && snapshot.Unavailable == 0})
	return string(raw), err
}

func (t *ProcessTools) Signal(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
	if t.Service == nil || tc.ProcessReview == nil {
		return "", safecmd.Reject(isolation.CodeApprovalUnavailable, nil)
	}
	refs, err := toolkit.ParseStringSliceArg(args, "references", 33)
	if err != nil {
		return "", err
	}
	if len(refs) > 32 {
		return "", safecmd.Reject("PROCESS_REQUEST_INVALID", map[string]any{"reason": "too many process references"})
	}
	if len(refs) == 0 {
		return "", toolkit.MissingArg("references")
	}
	signal, _ := args["signal"].(string)
	switch signal {
	case "TERM", "KILL", "INT", "HUP", "STOP", "CONT", "USR1", "USR2":
	default:
		return "", safecmd.Reject("PROCESS_REQUEST_INVALID", map[string]any{"reason": "unsupported signal"})
	}
	processes := make([]hostprocess.Process, 0, len(refs))
	seen := map[int]bool{}
	for _, ref := range refs {
		process, err := t.Service.Resolve(processTaskSession(tc), ref)
		if err != nil {
			return "", processError(err)
		}
		if seen[process.PID] {
			continue
		}
		seen[process.PID] = true
		processes = append(processes, process)
	}
	if err := tc.ProcessReview(ctx, "signal", processes); err != nil {
		return "", err
	}
	results := make([]map[string]any, 0, len(processes))
	for _, process := range processes {
		err := t.Service.Signal(ctx, process, signal)
		row := map[string]any{"pid": process.PID, "instance": process.Instance, "signal": signal, "delivered": err == nil}
		if err != nil {
			row["error"] = err.Error()
			row["code"] = tools.AsToolReject(processError(err)).Code
		}
		results = append(results, row)
	}
	raw, err := surveyjson.Marshal(map[string]any{"results": results})
	return string(raw), err
}

func processError(err error) error {
	code := "PROCESS_UNAVAILABLE"
	if errors.Is(err, hostprocess.ErrStale) {
		code = "PROCESS_REFERENCE_STALE"
	}
	if errors.Is(err, hostprocess.ErrUnsupported) {
		code = "PROCESS_UNSUPPORTED"
	}
	return safecmd.Reject(code, map[string]any{"reason": fmt.Sprint(err)})
}

func processTaskSession(tc tools.ToolContext) string {
	if root := strings.TrimSpace(tc.ParentSessionID); root != "" {
		return root
	}
	return strings.TrimSpace(tc.SessionID)
}
