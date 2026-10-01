package editorconfig

import (
	"strings"

	"github.com/aymanbagabas/go-udiff"
)

// Rule names a declared pair that written text can break.
type Rule string

const (
	RuleTrimTrailingWhitespace Rule = "trim_trailing_whitespace"
	RuleIndentStyle            Rule = "indent_style"
	RuleEndOfLine              Rule = "end_of_line"
	RuleInsertFinalNewline     Rule = "insert_final_newline"
)

// MaxReportedLines caps the line numbers kept per rule.
const MaxReportedLines = 20

// Violation is one declared rule broken by the lines a change wrote.
type Violation struct {
	Rule Rule
	// Lines are 1-based line numbers in the written text, at most MaxReportedLines.
	Lines []int
	// LineCount counts every offending line, including those past the cap.
	LineCount int
}

// Change is one text write. Before is nil when the write created the file.
type Change struct {
	Before *string
	After  string
}

// Check reports rules broken on lines the change inserted or rewrote.
func Check(p Properties, c Change) ([]Violation, error) {
	before := ""
	if c.Before != nil {
		before = *c.Before
	}
	if !p.Declared() || before == c.After {
		return nil, nil
	}
	written, err := writtenLines(before, c.After)
	if err != nil {
		return nil, err
	}
	found := map[Rule]*Violation{}
	order := []Rule{RuleTrimTrailingWhitespace, RuleIndentStyle, RuleEndOfLine, RuleInsertFinalNewline}
	add := func(rule Rule, line int) {
		v := found[rule]
		if v == nil {
			v = &Violation{Rule: rule}
			found[rule] = v
		}
		v.LineCount++
		if len(v.Lines) < MaxReportedLines {
			v.Lines = append(v.Lines, line)
		}
	}
	for _, line := range written {
		for _, rule := range lineRules(p, line.content) {
			add(rule, line.number)
		}
	}
	if finalNewlineBroken(p, c) {
		add(RuleInsertFinalNewline, strings.Count(strings.TrimSuffix(c.After, "\n"), "\n")+1)
	}
	var out []Violation
	for _, rule := range order {
		if v := found[rule]; v != nil {
			out = append(out, *v)
		}
	}
	return out, nil
}

type writtenLine struct {
	number int
	// content keeps its terminator, if any.
	content string
}

func writtenLines(before, after string) ([]writtenLine, error) {
	diff, err := udiff.ToUnifiedDiff("", "", before, udiff.Strings(before, after), 0)
	if err != nil {
		return nil, err
	}
	var out []writtenLine
	for _, hunk := range diff.Hunks {
		number := hunk.ToLine
		for _, line := range hunk.Lines {
			switch line.Kind {
			case udiff.Delete:
				continue
			case udiff.Insert:
				out = append(out, writtenLine{number: number, content: line.Content})
			default:
			}
			number++
		}
	}
	return out, nil
}

func lineRules(p Properties, content string) []Rule {
	body := strings.TrimSuffix(strings.TrimSuffix(content, "\n"), "\r")
	var rules []Rule
	if p.TrimTrailingWhitespace != nil && *p.TrimTrailingWhitespace && body != "" {
		if last := body[len(body)-1]; last == ' ' || last == '\t' {
			rules = append(rules, RuleTrimTrailingWhitespace)
		}
	}
	if indentBroken(p, body) {
		rules = append(rules, RuleIndentStyle)
	}
	switch p.EndOfLine {
	case EndOfLineLF:
		if strings.HasSuffix(content, "\r\n") {
			rules = append(rules, RuleEndOfLine)
		}
	case EndOfLineCRLF:
		if strings.HasSuffix(content, "\n") && !strings.HasSuffix(content, "\r\n") {
			rules = append(rules, RuleEndOfLine)
		}
	}
	return rules
}

// indentBroken flags a tab in space-style indentation, or a full level of
// leading spaces in tab style. Spaces after a tab are alignment.
func indentBroken(p Properties, body string) bool {
	lead := body[:len(body)-len(strings.TrimLeft(body, " \t"))]
	switch p.IndentStyle {
	case IndentSpace:
		return strings.Contains(lead, "\t")
	case IndentTab:
		return strings.HasPrefix(lead, strings.Repeat(" ", p.IndentWidth()))
	default:
		return false
	}
}

// finalNewlineBroken reports a final-newline state the change introduced.
// Empty files are exempt.
func finalNewlineBroken(p Properties, c Change) bool {
	if p.InsertFinalNewline == nil || c.After == "" {
		return false
	}
	want := *p.InsertFinalNewline
	if strings.HasSuffix(c.After, "\n") == want {
		return false
	}
	return c.Before == nil || *c.Before == "" || strings.HasSuffix(*c.Before, "\n") == want
}
