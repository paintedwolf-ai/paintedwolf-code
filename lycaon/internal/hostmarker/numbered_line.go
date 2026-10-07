package hostmarker

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// numberedLine is one rendered file line: number, separator, text. The host
// writes the colon form; retained evidence bodies also carry tab and pipe.
var numberedLine = regexp.MustCompile(`^ *(\d+)[:|\t](?: ?(.*))?$`)

// FormatNumberedLines renders page lines as every file-reading tool shows
// them, with the line number right-aligned in six columns.
func FormatNumberedLines(page []string, startLine int) string {
	if len(page) == 0 {
		return ""
	}
	var b strings.Builder
	for i, line := range page {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%6d: %s", startLine+i, line)
	}
	return b.String()
}

// NumberedLineAt returns the text of the rendered row numbered line within a
// body of numbered lines; ok is false when the body does not show that line.
func NumberedLineAt(body string, line int) (text string, ok bool) {
	if body == "" || line <= 0 {
		return "", false
	}
	for _, row := range strings.Split(body, "\n") {
		if n, rowText, parsed := ParseNumberedLine(row); parsed && n == line {
			return rowText, true
		}
	}
	return "", false
}

// ParseNumberedLine splits one rendered line; ok is false for unnumbered text.
func ParseNumberedLine(line string) (n int, text string, ok bool) {
	m := numberedLine.FindStringSubmatch(line)
	if m == nil {
		return 0, "", false
	}
	val, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, "", false
	}
	return val, m[2], true
}
