package worker

import (
	"strings"
)

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
