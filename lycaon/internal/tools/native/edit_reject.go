package native

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
)

func editOldStringNotFound(path, fileContent, oldString string) error {
	return &tools.ToolReject{
		Code: "EDIT_OLD_STRING_NOT_FOUND",
		Data: editMissData(path, fileContent, oldString),
	}
}

// editTargetChangedByOthers is the miss whose cause the ledger can state: the
// file changed during this session, and not by this session. The provenance
// rows are recorded facts; the miss itself may still also involve a misquote.
func editTargetChangedByOthers(
	path, fileContent, oldString string,
	changes []*surveyreceipt.SourceChange,
	totalChanges int,
) error {
	data := editMissData(path, fileContent, oldString)
	rows := make([]map[string]any, 0, len(changes))
	for _, change := range changes {
		row := map[string]any{"actor": change.Actor, "op": change.Op, "at": change.At}
		if change.Detail != "" {
			row["detail"] = change.Detail
		}
		rows = append(rows, row)
	}
	data["changed_by"] = rows
	data["change_count"] = totalChanges
	return &tools.ToolReject{Code: "EDIT_TARGET_CHANGED_BY_OTHERS", Data: data}
}

func editMissData(path, fileContent, oldString string) map[string]any {
	data := map[string]any{
		"path":             path,
		"old_string_chars": utf8.RuneCountInString(oldString),
		"total_lines":      toolkit.CountLines(fileContent),
	}
	if nearLine, ok := editNearLine(fileContent, oldString); ok {
		data["near_line"] = nearLine
		data["read_hint"] = fmtReadHint(path, nearLine)
	}
	if preview := editMismatchPreview(fileContent, oldString); preview != "" {
		data["file_preview"] = preview
	}
	return data
}

func editNearLine(fileContent, oldString string) (int, bool) {
	lines := toolkit.SplitLines(fileContent)
	if len(lines) == 0 {
		return 0, false
	}
	firstOld := strings.SplitN(oldString, "\n", 2)[0]
	if firstOld == "" {
		return 0, false
	}
	bestLine := 0
	bestScore := 0
	for i, line := range lines {
		score := editLineMatchScore(line, firstOld)
		if score > bestScore {
			bestScore = score
			bestLine = i + 1
		}
	}
	const minScore = 4
	if bestScore < minScore {
		return 0, false
	}
	return bestLine, true
}

func editLineMatchScore(line, needle string) int {
	if line == needle {
		return 100
	}
	if strings.Contains(line, needle) {
		return 60 + min(len(needle), 20)
	}
	if strings.Contains(needle, line) && len(line) >= 4 {
		return 50 + min(len(line), 20)
	}
	prefix := 0
	limit := min(len(line), len(needle))
	for prefix < limit && line[prefix] == needle[prefix] {
		prefix++
	}
	return prefix
}

func fmtReadHint(path string, nearLine int) string {
	start := nearLine - 3
	if start < 1 {
		start = 1
	}
	return "read(path=" + strconv.Quote(path) + ", offset=" + strconv.Itoa(start) + ", limit=20)"
}

func editOldStringAmbiguous(path string, occurrences int) error {
	return &tools.ToolReject{
		Code: "EDIT_OLD_STRING_AMBIGUOUS",
		Data: map[string]any{
			"path":        path,
			"occurrences": occurrences,
		},
	}
}

func editArgsConflict(detail string) error {
	return &tools.ToolReject{
		Code: "EDIT_ARGS_CONFLICT",
		Data: map[string]any{"detail": detail, "conflict": true},
	}
}

func editMismatchPreview(fileContent, oldString string) string {
	lines := toolkit.SplitLines(fileContent)
	if len(lines) == 0 {
		return ""
	}
	firstOld := strings.SplitN(oldString, "\n", 2)[0]
	if firstOld == "" {
		return truncateEditPreview(lines[0])
	}
	for _, line := range lines {
		if strings.Contains(line, firstOld) || strings.Contains(firstOld, line) {
			return truncateEditPreview(line)
		}
	}
	return truncateEditPreview(lines[0])
}

func truncateEditPreview(line string) string {
	const max = 120
	line = strings.TrimSpace(line)
	if len(line) <= max {
		return line
	}
	return runeclamp.ClampBytes(line, max)
}
