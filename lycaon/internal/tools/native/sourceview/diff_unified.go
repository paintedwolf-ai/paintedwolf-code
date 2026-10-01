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

type diffOp struct {
	tag  byte
	line string
}

func UnifiedDiff(fromName, toName string, oldLines, newLines []string, context int) string {
	if len(oldLines) == 0 && len(newLines) == 0 {
		return ""
	}
	if context < 0 {
		context = 0
	}
	ops := diffOpsFromLCS(oldLines, newLines)
	if len(ops) == 0 {
		return ""
	}
	hunks := buildUnifiedHunks(ops, context)
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
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.oldStart, h.oldCount, h.newStart, h.newCount)
		for idx := h.startOp; idx < h.endOp; idx++ {
			b.WriteByte(ops[idx].tag)
			b.WriteByte(' ')
			b.WriteString(ops[idx].line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func diffOpsFromLCS(a, b []string) []diffOp {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}
	i, j := n, m
	ops := make([]diffOp, 0, n+m)
	for i > 0 || j > 0 {
		switch {
		case i > 0 && j > 0 && a[i-1] == b[j-1]:
			ops = append([]diffOp{{tag: ' ', line: a[i-1]}}, ops...)
			i--
			j--
		case j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]):
			ops = append([]diffOp{{tag: '+', line: b[j-1]}}, ops...)
			j--
		default:
			ops = append([]diffOp{{tag: '-', line: a[i-1]}}, ops...)
			i--
		}
	}
	return ops
}

type unifiedHunk struct {
	oldStart int // 1-based
	oldCount int
	newStart int // 1-based
	newCount int
	startOp  int
	endOp    int // exclusive
}

func buildUnifiedHunks(ops []diffOp, context int) []unifiedHunk {
	type span struct{ start, end int }
	var changes []span
	for i, op := range ops {
		if op.tag != ' ' {
			changes = append(changes, span{i, i + 1})
		}
	}
	if len(changes) == 0 {
		return nil
	}
	merged := []span{changes[0]}
	for _, c := range changes[1:] {
		last := &merged[len(merged)-1]
		if c.start <= last.end+2*context+1 {
			if c.end > last.end {
				last.end = c.end
			}
			continue
		}
		merged = append(merged, c)
	}
	hunks := make([]unifiedHunk, 0, len(merged))
	for _, c := range merged {
		start := c.start - context
		if start < 0 {
			start = 0
		}
		end := c.end + context
		if end > len(ops) {
			end = len(ops)
		}
		h := unifiedHunk{startOp: start, endOp: end}
		oldLine, newLine := 1, 1
		for idx := 0; idx < h.startOp; idx++ {
			switch ops[idx].tag {
			case ' ':
				oldLine++
				newLine++
			case '-':
				oldLine++
			case '+':
				newLine++
			}
		}
		h.oldStart = oldLine
		h.newStart = newLine
		for idx := h.startOp; idx < h.endOp; idx++ {
			switch ops[idx].tag {
			case ' ':
				h.oldCount++
				h.newCount++
			case '-':
				h.oldCount++
			case '+':
				h.newCount++
			}
		}
		if h.oldCount == 0 {
			h.oldStart--
			if h.oldStart < 1 {
				h.oldStart = 1
			}
		}
		if h.newCount == 0 {
			h.newStart--
			if h.newStart < 1 {
				h.newStart = 1
			}
		}
		hunks = append(hunks, h)
	}
	return hunks
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
