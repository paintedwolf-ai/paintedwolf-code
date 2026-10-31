package gitcmd

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
	"github.com/lycaon/lycaon/internal/commandprogram"
)

// whitespaceCheck holds the added lines that break Git's default
// whitespace rules (blank-at-eol, space-before-tab) and the action chosen.
type whitespaceCheck struct {
	action string
	count  int
	// checking reports --check, where nothing is fixed and the default is
	// not to warn.
	checking bool
}

// checkWhitespace finds whitespace errors in added lines, reports the first
// five with their patch line, and applies --whitespace: error returns status
// 128, fix strips trailing whitespace from added lines.
func checkWhitespace(argv commandprogram.BoundArgv, sections []*applySection, stderr io.Writer) (*whitespaceCheck, int) {
	check := &whitespaceCheck{action: "warn", checking: has(argv, "--check")}
	if check.checking {
		check.action = "nowarn"
	}
	if action, ok := optionValue(argv, "--whitespace"); ok {
		check.action = action
	}
	switch check.action {
	case "nowarn", "warn", "fix", "error", "error-all":
	default:
		writeLine(stderr, "error: unrecognized whitespace option '"+check.action+"'")
		return check, 128
	}
	if check.action == "nowarn" || (reportOnly(argv) && !check.checking) {
		return check, 0
	}
	cursors := map[string]int{}
	for _, s := range sections {
		marker := byte('+')
		if has(argv, "-R") {
			marker = '-'
		}
		for _, frag := range s.file.TextFragments {
			for i, line := range frag.Lines {
				if line.Op != gitdiff.OpAdd {
					continue
				}
				problem := whitespaceProblem(line.Line)
				if problem == "" {
					continue
				}
				check.count++
				lineNo := locateAddedLine(s.input, marker, line.Line, cursors)
				if check.count <= 5 {
					writeLine(stderr, fmt.Sprintf("%s:%d: %s.\n%s", s.input.name, lineNo, problem, strings.TrimSuffix(line.Line, "\n")))
				}
				if check.action == "fix" && !check.checking {
					frag.Lines[i].Line = fixTrailing(line.Line)
				}
			}
		}
	}
	if check.count > 5 {
		writeLine(stderr, fmt.Sprintf("warning: squelched %d whitespace %s", check.count-5, pluralWord(check.count-5, "error", "errors")))
	}
	if check.count > 0 && (check.action == "error" || check.action == "error-all") {
		writeLine(stderr, fmt.Sprintf("error: %d %s whitespace errors.", check.count, pluralWord(check.count, "line adds", "lines add")))
		return check, 128
	}
	return check, 0
}

func (c *whitespaceCheck) summarize(w io.Writer) {
	switch {
	case c.count == 0:
	case c.action == "warn" || (c.action == "fix" && c.checking):
		writeLine(w, fmt.Sprintf("warning: %d %s whitespace errors.", c.count, pluralWord(c.count, "line adds", "lines add")))
	case c.action == "fix":
		writeLine(w, fmt.Sprintf("warning: %d %s after fixing whitespace errors.", c.count, pluralWord(c.count, "line applied", "lines applied")))
	}
}

func whitespaceProblem(line string) string {
	body := strings.TrimSuffix(line, "\n")
	if trimmed := strings.TrimRight(body, " \t\r"); trimmed != body {
		return "trailing whitespace"
	}
	indent := body[:len(body)-len(strings.TrimLeft(body, " \t"))]
	if strings.Contains(indent, " \t") {
		return "space before tab in indent"
	}
	return ""
}

func fixTrailing(line string) string {
	body := strings.TrimSuffix(line, "\n")
	fixed := strings.TrimRight(body, " \t\r")
	if len(body) != len(line) {
		fixed += "\n"
	}
	return fixed
}

// locateAddedLine finds an added line's 1-based line in its patch input,
// searching forward from the previous match so repeated lines resolve in
// order.
func locateAddedLine(input applyInput, marker byte, line string, cursors map[string]int) int {
	needle := append([]byte{'\n', marker}, line...)
	from := cursors[input.name]
	at := bytes.Index(input.data[from:], needle)
	if at < 0 {
		return 0
	}
	at += from + 1
	cursors[input.name] = at
	return bytes.Count(input.data[:at], []byte("\n")) + 1
}
