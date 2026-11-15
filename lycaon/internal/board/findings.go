package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func readFindings(ctx context.Context, args map[string]any, tctx tools.ToolContext, deps ToolDeps) (string, error) {
	if deps.Findings == nil || deps.Findings() == nil || deps.RootSession == nil {
		return "", fmt.Errorf("findings unavailable")
	}
	_, hasID := args["finding_id"]
	_, hasAfter := args["findings_after"]
	if hasID == hasAfter {
		return "", &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"field": "finding_id", "reason": "choose_one_findings_view"}}
	}
	for k := range args {
		if k != "finding_id" && k != "findings_after" {
			return "", &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"field": k, "reason": "unexpected_argument"}}
		}
	}
	store := deps.Findings()
	root := deps.RootSession(ctx, tctx.Identity.SessionID)
	if root == "" {
		return "", fmt.Errorf("findings session required")
	}
	if hasID {
		id, err := findingIndex(args["finding_id"])
		if err != nil {
			return "", err
		}
		if id < 1 {
			return "", &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"field": "finding_id", "reason": "positive_integer_required"}}
		}
		f, err := store.Get(ctx, root, id)
		if errors.Is(err, findings.ErrNotFound) {
			return "", &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"field": "finding_id", "reason": "finding_not_in_this_session"}}
		}
		if err != nil {
			return "", err
		}
		data, err := json.Marshal(findings.ToWire(f))
		return string(data), err
	}
	after, err := findingIndex(args["findings_after"])
	if err != nil {
		return "", err
	}
	rows, next, err := store.Recent(ctx, root, "", after, 9, time.Time{})
	if err != nil {
		return "", err
	}
	more := len(rows) > 8
	if more {
		rows = rows[:8]
		next = rows[len(rows)-1].ID
	}
	// Lists announce bodies; a single-finding read returns the full detail.
	wire := make([]api.Finding, 0, len(rows))
	for _, row := range rows {
		item := findings.ToWire(row)
		item.Body = ""
		wire = append(wire, item)
	}
	data, err := json.Marshal(map[string]any{"findings": wire, "next_after": next, "has_more": more})
	return string(data), err
}

func findingIndex(raw any) (int64, error) {
	switch v := raw.(type) {
	case float64:
		if v >= 0 && v == float64(int64(v)) {
			return int64(v), nil
		}
	case int:
		if v >= 0 {
			return int64(v), nil
		}
	case int64:
		if v >= 0 {
			return v, nil
		}
	}
	return 0, &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"field": "finding_id/findings_after", "reason": "nonnegative_integer_required"}}
}
