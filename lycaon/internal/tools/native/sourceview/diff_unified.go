package sourceview

import (
	"errors"
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

// DiffOp is one edit-script line: ' ' kept, '-' removed, '+' added. Old and
// New are zero-based indexes into the compared inputs (-1 when absent).
type DiffOp struct {
	Tag      byte
	Old, New int
}

// DiffHunk spans ops[StartOp:EndOp]; starts are one-based line numbers.
type DiffHunk struct {
	OldStart, OldCount, NewStart, NewCount int
	StartOp, EndOp                         int
}

// ErrDiffTooLarge reports a comparison above the caller's work ceiling.
var ErrDiffTooLarge = errors.New("diff comparison exceeds its work ceiling")

// DiffScript computes a longest-common-subsequence edit script. equal decides
// line identity (so whitespace-insensitive modes compare transformed lines
// while ops still index the original lines). Common prefix and suffix are
// matched directly; maxCells bounds the remaining comparison table, zero
// meaning unbounded.
func DiffScript(oldLines, newLines []string, equal func(a, b string) bool, maxCells int64) ([]DiffOp, error) {
	if equal == nil {
		equal = func(a, b string) bool { return a == b }
	}
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && equal(oldLines[prefix], newLines[prefix]) {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix && equal(oldLines[len(oldLines)-1-suffix], newLines[len(newLines)-1-suffix]) {
		suffix++
	}
	a, b := oldLines[prefix:len(oldLines)-suffix], newLines[prefix:len(newLines)-suffix]
	n, m := len(a), len(b)
	if maxCells > 0 && int64(n+1)*int64(m+1) > maxCells {
		return nil, ErrDiffTooLarge
	}
	width := m + 1
	dp := make([]int32, (n+1)*width)
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			switch {
			case equal(a[i-1], b[j-1]):
				dp[i*width+j] = dp[(i-1)*width+j-1] + 1
			case dp[(i-1)*width+j] >= dp[i*width+j-1]:
				dp[i*width+j] = dp[(i-1)*width+j]
			default:
				dp[i*width+j] = dp[i*width+j-1]
			}
		}
	}
	middle := make([]DiffOp, 0, n+m)
	for i, j := n, m; i > 0 || j > 0; {
		switch {
		case i > 0 && j > 0 && equal(a[i-1], b[j-1]):
			middle = append(middle, DiffOp{Tag: ' ', Old: prefix + i - 1, New: prefix + j - 1})
			i--
			j--
		case j > 0 && (i == 0 || dp[i*width+j-1] >= dp[(i-1)*width+j]):
			middle = append(middle, DiffOp{Tag: '+', Old: -1, New: prefix + j - 1})
			j--
		default:
			middle = append(middle, DiffOp{Tag: '-', Old: prefix + i - 1, New: -1})
			i--
		}
	}
	ops := make([]DiffOp, 0, prefix+len(middle)+suffix)
	for k := 0; k < prefix; k++ {
		ops = append(ops, DiffOp{Tag: ' ', Old: k, New: k})
	}
	for k := len(middle) - 1; k >= 0; k-- {
		ops = append(ops, middle[k])
	}
	for k := 0; k < suffix; k++ {
		ops = append(ops, DiffOp{Tag: ' ', Old: len(oldLines) - suffix + k, New: len(newLines) - suffix + k})
	}
	return ops, nil
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
