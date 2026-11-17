package native

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"
)

const (
	seamContextRadius  = 2
	seamContextMaxLine = 160
)

func truncateSeamLine(line string) string {
	return runeclamp.ClampBytes(line, seamContextMaxLine)
}

// mutationContext compares accepted states, including removals that an after-only
// seam cannot show. Common outer lines are omitted without a quadratic diff.
func mutationContext(before, after string) string {
	if before == after {
		return ""
	}
	old, next := strings.SplitAfter(before, "\n"), strings.SplitAfter(after, "\n")
	start := 0
	for start < len(old) && start < len(next) && old[start] == next[start] {
		start++
	}
	endOld, endNext := len(old), len(next)
	for endOld > start && endNext > start && old[endOld-1] == next[endNext-1] {
		endOld--
		endNext--
	}
	var b strings.Builder
	b.WriteString("Accepted change span (unchanged interior lines may appear):")
	appendMutationSide(&b, "Before", old[start:endOld], start+1)
	appendMutationSide(&b, "After", next[start:endNext], start+1)
	return b.String()
}

func appendMutationSide(b *strings.Builder, label string, lines []string, start int) {
	fmt.Fprintf(b, "\n%s (%d lines):", label, len(lines))
	const edge = 8
	for i, line := range lines {
		if i >= edge && i < len(lines)-edge {
			if i == edge {
				fmt.Fprintf(b, "\n… %d lines omitted …", len(lines)-2*edge)
			}
			continue
		}
		text := strings.TrimSuffix(line, "\n")
		fmt.Fprintf(b, "\n%d\t%s", start+i, truncateSeamLine(text))
		if line != "" && !strings.HasSuffix(line, "\n") {
			b.WriteString(" [no newline]")
		}
	}
}
