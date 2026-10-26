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
	for _, c := range merged {
		h := DiffHunk{StartOp: max(c.start-context, 0), EndOp: min(c.end+context, len(ops))}
		oldLine, newLine := 1, 1
		for _, op := range ops[:h.StartOp] {
			if op.Tag != '+' {
				oldLine++
			}
			if op.Tag != '-' {
				newLine++
			}
		}
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
