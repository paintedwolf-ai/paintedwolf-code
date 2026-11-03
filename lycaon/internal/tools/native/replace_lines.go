package native

import (
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"strings"
)

// spliceLineRange replaces an inclusive 1-based line range. Retained lines keep
// their own terminators; new lines take the file's dominant terminator.
func spliceLineRange(fileContent string, startLine, endLine int, newContent string) string {
	lines := splitKeepingTerminators(fileContent)
	terminator := dominantTerminator(fileContent)
	startIdx := startLine - 1
	endIdx := endLine // exclusive slice bound for inclusive endLine
	newLines := toolkit.SplitLines(newContent)
	tail := lines[endIdx:]

	var out strings.Builder
	for _, line := range lines[:startIdx] {
		out.WriteString(line)
	}
	for i, line := range newLines {
		out.WriteString(strings.TrimSuffix(line, "\r"))
		// The last inserted line ends the file only when nothing follows it;
		// then it carries a terminator exactly if the original file did.
		if i < len(newLines)-1 || len(tail) > 0 || strings.HasSuffix(fileContent, "\n") {
			out.WriteString(terminator)
		}
	}
	for _, line := range tail {
		out.WriteString(line)
	}
	return out.String()
}

// splitKeepingTerminators splits content into lines that keep their own line
// terminator. Line count matches toolkit.SplitLines: a trailing newline does
// not produce an extra empty line.
func splitKeepingTerminators(content string) []string {
	var out []string
	for len(content) > 0 {
		idx := strings.IndexByte(content, '\n')
		if idx < 0 {
			out = append(out, content)
			break
		}
		out = append(out, content[:idx+1])
		content = content[idx+1:]
	}
	return out
}

// dominantTerminator reports which terminator newly written lines take. CRLF
// needs a strict majority; ties and files without newlines take LF.
func dominantTerminator(content string) string {
	crlf := strings.Count(content, "\r\n")
	if crlf > strings.Count(content, "\n")-crlf {
		return "\r\n"
	}
	return "\n"
}

func replaceLinesInvalidRange(path string, startLine, endLine int, reason string) error {
	return &toolrejection.ToolReject{
		Code: "REPLACE_LINES_INVALID_RANGE",
		Data: map[string]any{
			"path":       path,
			"start_line": startLine,
			"end_line":   endLine,
			"reason":     reason,
		},
	}
}

func replaceLinesBeyondEOF(path string, startLine, endLine, totalLines int) error {
	return &toolrejection.ToolReject{
		Code: "REPLACE_LINES_BEYOND_EOF",
		Data: map[string]any{
			"path":        path,
			"start_line":  startLine,
			"end_line":    endLine,
			"total_lines": totalLines,
		},
	}
}

// ReplaceLineRange validates and applies a 1-based inclusive replacement.
func ReplaceLineRange(path, content string, startLine, endLine int, newContent string) (string, error) {
	if startLine <= 0 || endLine < startLine {
		return "", replaceLinesInvalidRange(path, startLine, endLine, "operation range must be 1-based inclusive")
	}
	totalLines := toolkit.CountLines(content)
	if (totalLines == 0 && (startLine != 1 || endLine != 1)) || (totalLines > 0 && endLine > totalLines) {
		return "", replaceLinesBeyondEOF(path, startLine, endLine, totalLines)
	}
	replaced := newContent
	if totalLines > 0 {
		replaced = spliceLineRange(content, startLine, endLine, newContent)
	}
	if err := guardMutationContent("replace_lines", path, replaced); err != nil {
		return "", err
	}
	return replaced, nil
}
