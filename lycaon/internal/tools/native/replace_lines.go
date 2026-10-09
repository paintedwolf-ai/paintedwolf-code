package native

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/syntaxhealth"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// ReplaceLinesTool replaces an inclusive 1-based line range with new_content.
type ReplaceLinesTool struct {
	Boundary     *sandbox.Boundary
	ContentApply ContentApplyGate
}

func (t *ReplaceLinesTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	var overrideErr error
	ctx, overrideErr = tools.WithSyntaxOverride(ctx, args)
	if overrideErr != nil {
		return "", overrideErr
	}
	path, _ := args["path"].(string)
	if path == "" {
		return "", toolkit.MissingArg("path")
	}
	if err := assertProfileWriteScope(ctx, t.Boundary, tctx, path, "replace_lines"); err != nil {
		return "", writeScopeReject(ctx, t.Boundary, path, tctx.ProfileID(), "replace_lines", err)
	}
	operations, err := parseLineOperations(path, args)
	if err != nil {
		return "", err
	}
	if err := beforeWorkerMutation(ctx, tctx, path); err != nil {
		return "", err
	}
	resolved, err := projectpaths.ResolveWrite(ctx, t.Boundary, tctx, path)
	if err != nil {
		return "", err
	}
	fullPath := resolved.Abs
	path = resolved.DisplayPath
	return retryEditorDocument(ctx, path, func() (string, error) {
		st, err := loadAgentSourceText(ctx, "replace_lines", tctx, resolved)
		if err != nil {
			if os.IsNotExist(err) {
				return "", sourceview.PathNotFound("replace_lines", path, fullPath)
			}
			return "", fmt.Errorf("read failed: %w", err)
		}
		old := st.Content
		totalLines := toolkit.CountLines(old)
		if err := validateLineOperations(path, operations, totalLines); err != nil {
			return "", err
		}
		replaced, err := applyLineOperations(path, old, operations)
		if err != nil {
			return "", err
		}
		if err := guardMutationContent("replace_lines", path, replaced); err != nil {
			return "", err
		}
		finalContent := replaced
		reportTextIntent(tctx, resolved, st, true, replaced)
		if t.ContentApply != nil {
			before := old
			var gateErr error
			finalContent, gateErr = t.ContentApply.GateApply(ctx, "replace_lines", path, &before, replaced, tctx)
			if gateErr != nil {
				return "", gateErr
			}
			if finalContent != replaced {
				reportTextIntent(tctx, resolved, st, true, finalContent)
			}
		}
		seam := operationsSeam(operations)
		if err := rejectIfSyntaxUnhealthy(ctx, "replace_lines", path, &old, finalContent, seam); err != nil {
			return "", err
		}
		landed, err := landEditedText(ctx, tctx, "replace_lines", resolved, st, finalContent)
		if err != nil {
			return "", err
		}
		beforeCopy := old
		captureFileEdit(tctx, path, finalContent, &beforeCopy)
		afterSuccessfulMutation(ctx, tctx, path)
		receipt := fmt.Sprintf("Applied %d atomic line operation(s) to %s", len(operations), path)
		if seams := seamContext(finalContent, seam.FinalStartLine, seam.FinalEndLine-seam.FinalStartLine+1); seams != "" {
			receipt += "\n" + seams
		}
		if note := landed.note(); note != "" {
			receipt += "\n" + note
		}
		return receipt, nil
	})
}

type lineOperation struct {
	Kind       string
	StartLine  int
	EndLine    int
	NewContent string
	Direction  string
	Prefix     string
}

const (
	maxLineNumber     = 1 << 30
	maxLineOperations = 64
)

func parseLineOperations(path string, args map[string]any) ([]lineOperation, error) {
	rawOperations, hasOperations := args["operations"].([]any)
	if !hasOperations {
		startLine, startOK := exactLineArg(args, "start_line")
		endLine, endOK := exactLineArg(args, "end_line")
		newContent, _ := args["new_content"].(string)
		if !startOK || !endOK {
			return nil, replaceLinesInvalidRange(path, startLine, endLine, "start_line and end_line are required (1-based inclusive)")
		}
		return []lineOperation{{Kind: "replace", StartLine: startLine, EndLine: endLine, NewContent: newContent}}, nil
	}
	if len(rawOperations) == 0 {
		return nil, replaceLinesInvalidRange(path, 0, 0, "operations must not be empty")
	}
	if len(rawOperations) > maxLineOperations {
		return nil, replaceLinesInvalidRange(path, 0, 0, "operations exceeds the 64-operation atomic limit")
	}
	operations := make([]lineOperation, 0, len(rawOperations))
	for _, raw := range rawOperations {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, replaceLinesInvalidRange(path, 0, 0, "each operation must be an object")
		}
		startLine, startOK := exactLineArg(item, "start_line")
		endLine, endOK := exactLineArg(item, "end_line")
		if !startOK || !endOK {
			return nil, replaceLinesInvalidRange(path, startLine, endLine, "operation range must be 1-based inclusive")
		}
		kind := strings.TrimSpace(lineStringArg(item, "kind"))
		direction := strings.TrimSpace(lineStringArg(item, "direction"))
		prefix := lineStringArg(item, "prefix")
		switch kind {
		case "indent":
			if direction == "" {
				direction = "indent"
			}
			if prefix == "" {
				prefix = "    "
			}
		case "dedent":
			if direction == "" {
				direction = "dedent"
			}
			if prefix == "" {
				prefix = "    "
			}
		}
		operation := lineOperation{
			Kind:       kind,
			StartLine:  startLine,
			EndLine:    endLine,
			NewContent: lineStringArg(item, "new_content"),
			Direction:  direction,
			Prefix:     prefix,
		}
		operations = append(operations, operation)
	}
	return operations, nil
}

func exactLineArg(args map[string]any, key string) (int, bool) {
	var value int
	switch raw := args[key].(type) {
	case int:
		value = raw
	case int64:
		if raw > maxLineNumber || raw < 1 {
			return 0, false
		}
		value = int(raw)
	case float64:
		if math.Trunc(raw) != raw || raw > maxLineNumber || raw < 1 {
			return 0, false
		}
		value = int(raw)
	default:
		return 0, false
	}
	return value, value >= 1 && value <= maxLineNumber
}

func lineStringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func validateLineOperations(path string, operations []lineOperation, totalLines int) error {
	sorted := append([]lineOperation(nil), operations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].StartLine < sorted[j].StartLine })
	previousEnd := 0
	for _, operation := range sorted {
		if operation.StartLine <= 0 || operation.EndLine < operation.StartLine {
			return replaceLinesInvalidRange(path, operation.StartLine, operation.EndLine, "operation range must be 1-based inclusive")
		}
		if totalLines == 0 {
			if len(sorted) != 1 || operation.Kind != "replace" || operation.StartLine != 1 || operation.EndLine != 1 {
				return replaceLinesBeyondEOF(path, operation.StartLine, operation.EndLine, totalLines)
			}
		} else if operation.EndLine > totalLines {
			return replaceLinesBeyondEOF(path, operation.StartLine, operation.EndLine, totalLines)
		}
		if operation.StartLine <= previousEnd {
			return replaceLinesInvalidRange(path, operation.StartLine, operation.EndLine, "operation ranges must not overlap")
		}
		switch operation.Kind {
		case "replace":
		case "shift_indent", "indent", "dedent":
			if operation.Direction != "indent" && operation.Direction != "dedent" {
				return replaceLinesInvalidRange(path, operation.StartLine, operation.EndLine, "indent operation direction must be indent or dedent")
			}
			if operation.Prefix == "" || strings.Trim(operation.Prefix, " \t") != "" {
				return replaceLinesInvalidRange(path, operation.StartLine, operation.EndLine, "indent operation prefix must contain only spaces or tabs")
			}
		default:
			return replaceLinesInvalidRange(path, operation.StartLine, operation.EndLine, "operation kind must be replace, shift_indent, indent, or dedent")
		}
		previousEnd = operation.EndLine
	}
	return nil
}

func applyLineOperations(path, content string, operations []lineOperation) (string, error) {
	if toolkit.CountLines(content) == 0 {
		return operations[0].NewContent, nil
	}
	sorted := append([]lineOperation(nil), operations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].StartLine > sorted[j].StartLine })
	result := content
	for _, operation := range sorted {
		replacement := operation.NewContent
		if operation.Kind == "shift_indent" || operation.Kind == "indent" || operation.Kind == "dedent" {
			selected := toolkit.SplitLines(result)[operation.StartLine-1 : operation.EndLine]
			shifted, err := shiftIndent(path, selected, operation)
			if err != nil {
				return "", err
			}
			replacement = strings.Join(shifted, "\n")
		}
		result = spliceLineRange(result, operation.StartLine, operation.EndLine, replacement)
	}
	return result, nil
}

func shiftIndent(path string, lines []string, operation lineOperation) ([]string, error) {
	out := append([]string(nil), lines...)
	for i, line := range out {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if operation.Direction == "indent" {
			out[i] = operation.Prefix + line
			continue
		}
		if !strings.HasPrefix(line, operation.Prefix) {
			return nil, &toolrejection.ToolReject{Code: "REPLACE_LINES_INDENT_MISMATCH", Data: map[string]any{
				"path": path, "line": operation.StartLine + i, "prefix": operation.Prefix,
			}}
		}
		out[i] = strings.TrimPrefix(line, operation.Prefix)
	}
	return out, nil
}

func operationsSeam(operations []lineOperation) mutationSeam {
	sorted := append([]lineOperation(nil), operations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].StartLine < sorted[j].StartLine })
	seam := mutationSeam{
		OriginalStartLine: sorted[0].StartLine,
		OriginalEndLine:   sorted[len(sorted)-1].EndLine,
	}
	lineOffset := 0
	for _, operation := range sorted {
		newLines := operation.EndLine - operation.StartLine + 1
		if operation.Kind == "replace" {
			newLines = toolkit.CountLines(operation.NewContent)
		}
		start := operation.StartLine + lineOffset
		end := start + max(newLines, 1) - 1
		seam.Ranges = append(seam.Ranges, syntaxhealth.LineRange{Start: start, End: end})
		if seam.FinalStartLine == 0 {
			seam.FinalStartLine = start
		}
		seam.FinalEndLine = max(seam.FinalEndLine, end)
		lineOffset += newLines - (operation.EndLine - operation.StartLine + 1)
	}
	return seam
}

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
