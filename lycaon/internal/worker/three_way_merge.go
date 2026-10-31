package worker

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

// ThreeWayMerge performs a line-oriented 3-way merge (base, primary/ours, branch/theirs).
func ThreeWayMerge(base, ours, theirs string) (merged string, hunks []api.WorkerMergeHunk, clean bool) {
	if ours == theirs {
		return ours, nil, true
	}
	if ours == base {
		return theirs, nil, true
	}
	if theirs == base {
		return ours, nil, true
	}
	bl := splitMergeLines(base)
	ol := splitMergeLines(ours)
	tl := splitMergeLines(theirs)
	out, conflicts := merge3Lines(bl, ol, tl)
	return joinMergeLines(out), conflicts, len(conflicts) == 0
}

func splitMergeLines(s string) []string {
	if s == "" {
		return nil
	}
	normalized := strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	parts := strings.Split(normalized, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func joinMergeLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

func lineAt(lines []string, i int) string {
	if i >= 0 && i < len(lines) {
		return lines[i]
	}
	return ""
}

func mergeAligned(base, ours, theirs []string, i int) (out string, ok bool) {
	b, o, t := lineAt(base, i), lineAt(ours, i), lineAt(theirs, i)
	switch {
	case b != "" && b == o && b == t:
		return b, true
	case b != "" && b == o && b != t:
		return t, true
	case b != "" && b == t && b != o:
		return o, true
	case o != "" && o == t:
		return o, true
	default:
		return "", false
	}
}

func merge3Lines(base, ours, theirs []string) (out []string, hunks []api.WorkerMergeHunk) {
	maxLen := len(base)
	if len(ours) > maxLen {
		maxLen = len(ours)
	}
	if len(theirs) > maxLen {
		maxLen = len(theirs)
	}
	for i := 0; i < maxLen; {
		if merged, ok := mergeAligned(base, ours, theirs, i); ok {
			out = append(out, merged)
			i++
			continue
		}
		start := len(out) + 1
		var bLines, oLines, tLines []string
		for i < maxLen {
			if _, ok := mergeAligned(base, ours, theirs, i); ok {
				break
			}
			bLines = append(bLines, lineAt(base, i))
			oLines = append(oLines, lineAt(ours, i))
			tLines = append(tLines, lineAt(theirs, i))
			i++
		}
		oText := strings.Join(trimEmptyEdges(oLines), "\n")
		tText := strings.Join(trimEmptyEdges(tLines), "\n")
		bText := strings.Join(trimEmptyEdges(bLines), "\n")
		if oText == tText && oText != "" {
			out = append(out, oLines...)
			continue
		}
		out = append(out,
			"<<<<<<< primary",
			oText,
			"=======",
			tText,
			">>>>>>> branch",
		)
		hunks = append(hunks, api.WorkerMergeHunk{
			StartLine: start,
			EndLine:   len(out),
			Base:      bText,
			Primary:   oText,
			Branch:    tText,
		})
	}
	return out, hunks
}

func trimEmptyEdges(lines []string) []string {
	start := 0
	for start < len(lines) && lines[start] == "" {
		start++
	}
	end := len(lines)
	for end > start && lines[end-1] == "" {
		end--
	}
	return lines[start:end]
}

const promoteBranchDeltaMaxBytes = 4096

// BranchDelta renders a unified primary→branch diff (language-agnostic line diff).
func BranchDelta(primary, branch string) string {
	if primary == branch {
		return ""
	}
	pLines := splitMergeLines(primary)
	bLines := splitMergeLines(branch)
	var out strings.Builder
	i, j := 0, 0
	for i < len(pLines) || j < len(bLines) {
		if i < len(pLines) && j < len(bLines) && pLines[i] == bLines[j] {
			i++
			j++
			continue
		}
		if j < len(bLines) && (i >= len(pLines) || (i+1 < len(pLines) && j+1 < len(bLines) && pLines[i+1] == bLines[j])) {
			out.WriteString("+ " + bLines[j] + "\n")
			j++
			continue
		}
		if i < len(pLines) && (j >= len(bLines) || (i+1 < len(pLines) && j+1 < len(bLines) && pLines[i] == bLines[j+1])) {
			out.WriteString("- " + pLines[i] + "\n")
			i++
			continue
		}
		if i < len(pLines) {
			out.WriteString("- " + pLines[i] + "\n")
			i++
		}
		if j < len(bLines) {
			out.WriteString("+ " + bLines[j] + "\n")
			j++
		}
		if out.Len() >= promoteBranchDeltaMaxBytes {
			break
		}
	}
	s := strings.TrimSpace(out.String())
	if out.Len() >= promoteBranchDeltaMaxBytes {
		s += "\n…"
	}
	return s
}

func buildConflictSummary(c PromoteConflict) []string {
	var out []string
	maxLines := tooloutput.MaxInlineConflictSummaryLines
	for i, h := range c.Hunks {
		if i >= maxLines {
			out = append(out, "…")
			break
		}
		if line := formatHunkSummaryLine(h); line != "" {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		if delta := strings.TrimSpace(BranchDelta(c.Primary, c.Branch)); delta != "" {
			for i, line := range strings.Split(delta, "\n") {
				if i >= maxLines-1 {
					out = append(out, "…")
					break
				}
				line = strings.TrimSpace(line)
				if line != "" {
					out = append(out, line)
				}
			}
		}
	}
	return out
}

func formatHunkSummaryLine(h api.WorkerMergeHunk) string {
	end := h.EndLine
	if end <= 0 {
		end = h.StartLine
	}
	rangeLabel := fmt.Sprintf("L%d", h.StartLine)
	if end > h.StartLine {
		rangeLabel = fmt.Sprintf("L%d-%d", h.StartLine, end)
	}
	var parts []string
	if line := strings.TrimSpace(h.Primary); line != "" {
		parts = append(parts, "primary: "+truncateConflictLine(line))
	}
	if line := strings.TrimSpace(h.Branch); line != "" {
		parts = append(parts, "branch: "+truncateConflictLine(line))
	}
	if len(parts) == 0 {
		return rangeLabel
	}
	return rangeLabel + ": " + strings.Join(parts, " | ")
}

func truncateConflictLine(s string) string {
	const maxBytes = 120
	return runeclamp.ClampBytes(strings.TrimSpace(s), maxBytes)
}
