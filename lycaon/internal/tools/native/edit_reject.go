package native

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
)

// EditArgsConflict refuses edit arguments that cannot name one replacement.
func EditArgsConflict(detail string) error {
	return &toolrejection.ToolReject{
		Code: "EDIT_ARGS_CONFLICT",
		Data: map[string]any{"detail": detail, "conflict": true},
	}
}

// EditMiss refuses an old_string that is not in the file. When the ledger recorded
// another actor's change during this session it states them, as recorded facts: the
// miss itself may still be a misquote.
func EditMiss(ctx context.Context, tc tools.ToolContext, path, abs, content, oldString, newString string) error {
	data := map[string]any{
		"path":                   path,
		"old_string_chars":       utf8.RuneCountInString(oldString),
		"total_lines":            toolkit.CountLines(content),
		"source_history_offered": tools.OffersSourceHistory(tc),
	}
	if newString != "" {
		data["replacement_matches"] = strings.Count(content, newString)
		if at := strings.Index(content, newString); at >= 0 {
			data["replacement_line"] = strings.Count(content[:at], "\n") + 1
		}
	}
	changes, total, ok := sourceview.ForeignChanges(ctx, tc, abs)
	if !ok {
		return &toolrejection.ToolReject{Code: "EDIT_OLD_STRING_NOT_FOUND", Data: data}
	}
	rows := make([]map[string]any, 0, len(changes))
	for _, change := range changes {
		row := map[string]any{"actor": change.Actor, "op": change.Op, "at": change.At}
		if change.Detail != "" {
			row["detail"] = change.Detail
		}
		rows = append(rows, row)
	}
	data["changed_by"] = rows
	data["change_count"] = total
	return &toolrejection.ToolReject{Code: "EDIT_TARGET_CHANGED_BY_OTHERS", Data: data}
}
