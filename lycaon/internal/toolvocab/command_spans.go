package toolvocab

import (
	"regexp"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
)

// InlineBacktickSpan is one inline backtick span extracted outside fenced code blocks.
type InlineBacktickSpan struct {
	Span  string
	Line  int
	Start int
	End   int
}

// CommandSpan is an inline backtick span containing one or more declared command segments.
type CommandSpan struct {
	Span     string
	Segments []string
	Line     int
	Start    int
	End      int
}

var (
	commandChainSplitRe  = regexp.MustCompile(`\s*(?:&&|\|\||[|;])\s*`)
	declaredCommandsOnce = sync.OnceValue(loadDeclaredCommands)
)

func loadDeclaredCommands() map[string]bool {
	out := map[string]bool{}
	lang, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return out
	}
	for _, td := range lang.Tools {
		for _, cf := range td.CommandForms {
			out[cf] = true
		}
	}
	for _, cmd := range lang.Commands {
		out[cmd.Name] = true
		for _, al := range cmd.Aliases {
			out[al] = true
		}
	}
	return out
}

// IsDeclaredCommand reports whether name is a declared command or alias in the
// host command language.
func IsDeclaredCommand(name string) bool {
	return declaredCommandsOnce()[name]
}

// SplitCommandChain splits a shell command line on &&, ||, |, and ;.
func SplitCommandChain(raw string) []string {
	return commandChainSplitRe.Split(raw, -1)
}

// SplitCommandWords tokenizes one command segment into shell-quoted words.
func SplitCommandWords(seg string) []string {
	var words []string
	var cur strings.Builder
	inWord := false
	var quote byte
	for i := 0; i < len(seg); i++ {
		ch := seg[i]
		if quote != 0 {
			if ch == '\\' && quote == '"' && i+1 < len(seg) {
				i++
				cur.WriteByte(seg[i])
				continue
			}
			if ch == quote {
				quote = 0
				continue
			}
			cur.WriteByte(ch)
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			inWord = true
			continue
		}
		if ch == ' ' || ch == '\t' {
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
			continue
		}
		cur.WriteByte(ch)
		inWord = true
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words
}

// FirstCommandWord returns the leading word of a command segment, or empty if none.
func FirstCommandWord(seg string) string {
	words := SplitCommandWords(strings.TrimSpace(seg))
	if len(words) == 0 {
		return ""
	}
	return words[0]
}

// ExtractInlineBackticks extracts inline backtick spans outside fenced code blocks.
func ExtractInlineBackticks(prose string) []InlineBacktickSpan {
	var out []InlineBacktickSpan
	inFence := ""
	lineOffset := 0
	for idx, lineText := range strings.Split(prose, "\n") {
		lineNum := idx + 1
		trimmed := strings.TrimSpace(lineText)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			switch inFence {
			case "":
				inFence = marker
			case marker:
				inFence = ""
			}
			lineOffset += len(lineText) + 1
			continue
		}
		if inFence == "" {
			out = append(out, scanLineBackticks(lineText, lineNum, lineOffset)...)
		}
		lineOffset += len(lineText) + 1
	}
	return out
}

func scanLineBackticks(line string, lineNum int, baseOffset int) []InlineBacktickSpan {
	var out []InlineBacktickSpan
	i := 0
	for i < len(line) {
		if line[i] != '`' {
			i++
			continue
		}
		ticks := 1
		for i+ticks < len(line) && line[i+ticks] == '`' {
			ticks++
		}
		start := i + ticks
		closing := strings.Repeat("`", ticks)
		relEnd := strings.Index(line[start:], closing)
		if relEnd < 0 {
			break
		}
		rawEnd := start + relEnd + ticks
		span := strings.TrimSpace(line[start : start+relEnd])
		if span != "" {
			out = append(out, InlineBacktickSpan{
				Span:  span,
				Line:  lineNum,
				Start: baseOffset + i,
				End:   baseOffset + rawEnd,
			})
		}
		i = rawEnd
	}
	return out
}

// CommandSegmentsInSpan returns the declared command segments inside one inline
// backtick span. Spans containing '(' or lacking a space are not command forms.
func CommandSegmentsInSpan(span string) []string {
	raw := strings.TrimSpace(span)
	if strings.Contains(raw, "(") || !strings.Contains(raw, " ") {
		return nil
	}
	var segs []string
	for _, seg := range SplitCommandChain(raw) {
		if first := FirstCommandWord(seg); first != "" && IsDeclaredCommand(first) {
			segs = append(segs, seg)
		}
	}
	return segs
}

// UnloadedCommandSpans lists inline command spans in text that are not preceded
// in their block by an exact request_tools(need="command") call.
func UnloadedCommandSpans(c *Catalog, text string) []CommandSpan {
	var out []CommandSpan
	for _, b := range Blocks(text) {
		calls := Calls(c, b.Text)
		for _, bs := range ExtractInlineBackticks(b.Text) {
			segs := CommandSegmentsInSpan(bs.Span)
			if len(segs) == 0 {
				continue
			}
			if !LoadedBefore(c, calls, toolreach.ProgramTool, bs.Start) {
				out = append(out, CommandSpan{
					Span:     bs.Span,
					Segments: segs,
					Line:     b.Line + strings.Count(b.Text[:max(bs.Start, 0)], "\n"),
					Start:    bs.Start,
					End:      bs.End,
				})
			}
		}
	}
	return out
}
