package native

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
)

const (
	seamContextRadius  = 2
	seamContextMaxLine = 160
)

// seamContext renders numbered lines around the splice seams of a line replacement.
func seamContext(content string, startLine, linesAdded int) string {
	lines := toolkit.SplitLines(content)
	total := len(lines)
	if total == 0 {
		return ""
	}
	topSeam := startLine
	bottomSeam := startLine + linesAdded - 1
	if bottomSeam < topSeam {
		bottomSeam = topSeam
	}
	windows := [][2]int{
		clampWindow(topSeam-seamContextRadius, topSeam+seamContextRadius, total),
	}
	bottom := clampWindow(bottomSeam-seamContextRadius, bottomSeam+seamContextRadius, total)
	if bottom[0] > windows[0][1]+1 {
		windows = append(windows, bottom)
	} else if bottom[1] > windows[0][1] {
		windows[0][1] = bottom[1]
	}
	var b strings.Builder
	b.WriteString("Seam context (after edit):")
	for i, w := range windows {
		if i > 0 {
			b.WriteString("\n" + runeclamp.Marker)
		}
		for n := w[0]; n <= w[1]; n++ {
			fmt.Fprintf(&b, "\n%d\t%s", n, truncateSeamLine(lines[n-1]))
		}
	}
	return b.String()
}

func clampWindow(start, end, total int) [2]int {
	if start < 1 {
		start = 1
	}
	if end > total {
		end = total
	}
	if end < start {
		end = start
	}
	return [2]int{start, end}
}

func truncateSeamLine(line string) string {
	return runeclamp.ClampBytes(line, seamContextMaxLine)
}
