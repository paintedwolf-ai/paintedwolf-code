package literal

import (
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/pattern"

	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
)

// diffExcludes compiles the -x patterns and the lines of each -X file as
// base-name globs.
func (inv *invocation) diffExcludes() ([]*regexp.Regexp, error) {
	var patterns []string
	for _, option := range inv.argv.Options("-x") {
		patterns = append(patterns, option.Values[0].Value)
	}
	for _, option := range inv.argv.Options("-X") {
		in, err := inv.openField(option.Values[0], true)
		if err != nil || in == nil {
			return nil, err
		}
		for _, line := range strings.Split(string(in.data), "\n") {
			if line != "" {
				patterns = append(patterns, line)
			}
		}
	}
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, value := range patterns {
		expr, err := pattern.Regexp(value, pattern.EntireString)
		if err == nil {
			var re *regexp.Regexp
			if re, err = regexp.Compile("(?s)" + expr); err == nil {
				out = append(out, re)
				continue
			}
		}
		return nil, inv.usage(value, "a shell glob for -x")
	}
	return out, nil
}

func (o diffOptions) excluded(name string) bool {
	for _, re := range o.excludes {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}

// diffHeader is the tree header's command: "diff" with the option words as
// given, each value word after the option that takes it, shell-quoted.
func diffHeader(inv *invocation) string {
	words := []string{"diff"}
	argv := inv.request.Argv.Values()[1:]
	for i := 0; i < len(argv); i++ {
		word := argv[i]
		if word == "--" {
			break
		}
		if len(word) < 2 || word[0] != '-' {
			continue
		}
		words = append(words, shellQuote(word))
		separate := strings.HasPrefix(word, "--") && slices.Contains([]string{"--exclude", "--exclude-from", "--unified"}, word) ||
			!strings.HasPrefix(word, "--") && strings.IndexByte("UxX", word[len(word)-1]) >= 0
		if separate && i+1 < len(argv) {
			words = append(words, shellQuote(argv[i+1]))
			i++
		}
	}
	return strings.Join(words, " ")
}

// equal compares transformed lines; reported lines stay original.
func (o diffOptions) equal() func(a, b string) bool {
	if !o.allSpace && !o.spaceChange {
		return nil
	}
	return func(a, b string) bool { return o.normalize(a) == o.normalize(b) }
}

func (o diffOptions) normalize(line string) string {
	body, eol := strings.CutSuffix(line, "\n")
	var b strings.Builder
	space := false
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case ' ', '\t', '\r', '\v', '\f':
			space = true
			continue
		}
		if space && o.spaceChange && !o.allSpace && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteByte(body[i])
	}
	if eol {
		b.WriteByte('\n')
	}
	return b.String()
}

// changed reports the ops that count as differences: with -B, a change run
// made only of blank lines is ignored.
func (o diffOptions) changed(ops []sourceview.DiffOp, oldLines, newLines []string) func(int) bool {
	ignored := map[int]bool{}
	if o.blankLines {
		for start := 0; start < len(ops); {
			if ops[start].Tag == ' ' {
				start++
				continue
			}
			end, blank := start, true
			for ; end < len(ops) && ops[end].Tag != ' '; end++ {
				var line string
				if ops[end].Tag == '+' {
					line = newLines[ops[end].New]
				} else {
					line = oldLines[ops[end].Old]
				}
				blank = blank && strings.TrimSpace(line) == ""
			}
			if blank {
				for i := start; i < end; i++ {
					ignored[i] = true
				}
			}
			start = end
		}
	}
	return func(i int) bool { return ops[i].Tag != ' ' && !ignored[i] }
}
