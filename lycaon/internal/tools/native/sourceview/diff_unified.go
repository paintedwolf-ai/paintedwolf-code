package sourceview

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
)

func SplitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// DiffHunk spans ops[StartOp:EndOp]; starts are one-based line numbers.
type DiffHunk struct {
	OldStart, OldCount, NewStart, NewCount int
	StartOp, EndOp                         int
}

// DiffHunks groups changed ops with context lines; changes closer than
// 2*context+1 kept lines share one hunk. changed decides which op indexes count as
// changes, so ignored changes neither open nor extend a hunk.
func DiffHunks(ops []DiffOp, context int, changed func(index int) bool) []DiffHunk {
	if context < 0 {
		context = 0
	}
	if changed == nil {
		changed = func(i int) bool { return ops[i].Tag != ' ' }
	}
	type span struct{ start, end int }
	var merged []span
	for i := range ops {
		if !changed(i) {
			continue
		}
		if len(merged) > 0 && i <= merged[len(merged)-1].end+2*context {
			merged[len(merged)-1].end = i + 1
			continue
		}
		merged = append(merged, span{i, i + 1})
	}
	hunks := make([]DiffHunk, 0, len(merged))
	oldLine, newLine, counted := 1, 1, 0
	for _, c := range merged {
		h := DiffHunk{StartOp: max(c.start-context, 0), EndOp: min(c.end+context, len(ops))}
		for _, op := range ops[counted:h.StartOp] {
			if op.Tag != '+' {
				oldLine++
			}
			if op.Tag != '-' {
				newLine++
			}
		}
		counted = h.StartOp
		h.OldStart, h.NewStart = oldLine, newLine
		for _, op := range ops[h.StartOp:h.EndOp] {
			if op.Tag != '+' {
				h.OldCount++
			}
			if op.Tag != '-' {
				h.NewCount++
			}
		}
		hunks = append(hunks, h)
	}
	return hunks
}

// UnifiedDiff renders the structured tools' unified preview.
func UnifiedDiff(fromName, toName string, oldLines, newLines []string, context int) string {
	if len(oldLines) == 0 && len(newLines) == 0 {
		return ""
	}
	ops, _ := DiffScript(oldLines, newLines, nil, 0)
	return renderUnified(fromName, toName, ops, oldLines, newLines, context)
}

// lineTableCells is one line's string header in DiffScript's four-byte cells, and
// lineScriptCells its entry in the output script.
const (
	lineTableCells  = 4
	lineScriptCells = 6
)

// UnifiedDiffWithin is UnifiedDiff under a ceiling in DiffScript's cells, line
// tables included, and returns ErrDiffTooLarge above it. A side that only grew or
// shrank needs no edit search, so its cost stays linear in the file.
func UnifiedDiffWithin(fromName, toName, oldText, newText string, context int, maxCells int64) (string, error) {
	lines := int64(strings.Count(oldText, "\n") + strings.Count(newText, "\n") + 2)
	tables := lines * lineTableCells
	if needed := tables + lines*lineScriptCells; needed > maxCells {
		return "", &DiffLimitError{maxCells, needed}
	}
	oldLines, newLines := SplitLines(oldText), SplitLines(newText)
	if len(oldLines) == 0 && len(newLines) == 0 {
		return "", nil
	}
	ops, ok := edgeOnlyScript(oldLines, newLines)
	if !ok {
		var err error
		if ops, err = DiffScript(oldLines, newLines, nil, maxCells-tables); err != nil {
			return "", err
		}
	}
	return renderUnified(fromName, toName, ops, oldLines, newLines, context), nil
}

// edgeOnlyScript answers without a search when, past the common edges, one side
// has nothing left. It builds the script DiffScript would.
func edgeOnlyScript(oldLines, newLines []string) ([]DiffOp, bool) {
	prefix, suffix := commonEdges(oldLines, newLines, func(a, b string) bool { return a == b })
	removed, added := len(oldLines)-prefix-suffix, len(newLines)-prefix-suffix
	if removed > 0 && added > 0 {
		return nil, false
	}
	ops := make([]DiffOp, 0, prefix+removed+added+suffix)
	for i := range prefix {
		ops = append(ops, DiffOp{' ', i, i})
	}
	for i := range removed {
		ops = append(ops, DiffOp{'-', prefix + i, -1})
	}
	for i := range added {
		ops = append(ops, DiffOp{'+', -1, prefix + i})
	}
	for i := range suffix {
		ops = append(ops, DiffOp{' ', prefix + removed + i, prefix + added + i})
	}
	return ops, true
}

func renderUnified(fromName, toName string, ops []DiffOp, oldLines, newLines []string, context int) string {
	hunks := DiffHunks(ops, context, nil)
	if len(hunks) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("--- ")
	b.WriteString(fromName)
	b.WriteString("\n+++ ")
	b.WriteString(toName)
	b.WriteString("\n")
	for _, h := range hunks {
		oldStart, newStart := h.OldStart, h.NewStart
		if h.OldCount == 0 {
			oldStart = max(oldStart-1, 1)
		}
		if h.NewCount == 0 {
			newStart = max(newStart-1, 1)
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", oldStart, h.OldCount, newStart, h.NewCount)
		for _, op := range ops[h.StartOp:h.EndOp] {
			b.WriteByte(op.Tag)
			b.WriteByte(' ')
			if op.Tag == '+' {
				b.WriteString(newLines[op.New])
			} else {
				b.WriteString(oldLines[op.Old])
			}
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func TruncateDiffOutput(s string, maxBytes int) (string, bool) {
	if len(s) <= maxBytes {
		return s, false
	}
	cut := s[:maxBytes]
	if idx := strings.LastIndex(cut, "\n"); idx > maxBytes/2 {
		cut = cut[:idx+1]
		return cut, true
	}
	// No newline to cut on, so the byte cap can land mid-rune.
	return string(toolkit.TrimPartialTrailingRune([]byte(cut))), true
}
