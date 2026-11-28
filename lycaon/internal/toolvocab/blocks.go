package toolvocab

import (
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/toolreach"
)

// Block is one paragraph, list item, heading, or table row of rendered copy
// outside fenced code. Line is the 1-based line where it starts.
type Block struct {
	Text string
	Line int
}

// Blocks splits rendered Markdown into the blocks claims are read in.
func Blocks(text string) []Block {
	var out []Block
	var cur []string
	start := 0
	flush := func() {
		if len(cur) > 0 {
			out = append(out, Block{Text: strings.Join(cur, "\n"), Line: start})
			cur = nil
		}
	}
	fence := ""
	for i, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			flush()
			switch marker := trimmed[:3]; fence {
			case "":
				fence = marker
			case marker:
				fence = ""
			}
			continue
		}
		if fence != "" || trimmed == "" {
			flush()
			continue
		}
		standalone := strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "|")
		if standalone || listItemStart.MatchString(trimmed) {
			flush()
		}
		if len(cur) == 0 {
			start = i + 1
		}
		cur = append(cur, line)
		if standalone {
			flush()
		}
	}
	flush()
	return out
}

var listItemStart = regexp.MustCompile(`^(?:[-*+]|[0-9]+[.)])\s`)

// Call is a call expression `tool(…)` naming a known tool, with byte offsets
// into the scanned text and the unparsed argument body.
type Call struct {
	Tool       string
	Start, End int
	Body       string
}

var callOpen = regexp.MustCompile(`\b([a-z][a-z0-9_]*)\(`)

// Calls lists the call expressions in text that name a known tool. A call's
// argument list must close on the line it opens.
func Calls(c *Catalog, text string) []Call {
	if c == nil {
		return nil
	}
	return CallsOf(c.KnownName, text)
}

// CallsOf lists the call expressions in text whose tool name known accepts.
func CallsOf(known func(string) bool, text string) []Call {
	var out []Call
	for _, loc := range callOpen.FindAllStringSubmatchIndex(text, -1) {
		tool := text[loc[2]:loc[3]]
		if !known(tool) {
			continue
		}
		if n, ok := callBodyLen(text[loc[1]:]); ok {
			out = append(out, Call{Tool: tool, Start: loc[0], End: loc[1] + n + 1, Body: text[loc[1] : loc[1]+n]})
		}
	}
	return out
}

func callBodyLen(rest string) (int, bool) {
	depth := 0
	var quote byte
	escaped := false
	for i := 0; i < len(rest); i++ {
		ch := rest[i]
		if ch == '\n' {
			return 0, false
		}
		if quote != 0 {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == quote:
				quote = 0
			}
			continue
		}
		switch ch {
		case '"', '\'':
			quote = ch
		case '(', '[', '{':
			depth++
		case ']', '}':
			depth = max(depth-1, 0)
		case ')':
			if depth == 0 {
				return i, true
			}
			depth--
		}
	}
	return 0, false
}

var exactNeed = regexp.MustCompile(`^need\s*[=:]\s*"([a-z][a-z0-9_]*)"$`)

// RequestedTool returns the tool an exact request_tools(need="<tool>") call
// loads. A need that describes an operation names no tool.
func RequestedTool(c *Catalog, call Call) (string, bool) {
	if call.Tool != toolreach.LoaderTool {
		return "", false
	}
	m := exactNeed.FindStringSubmatch(strings.TrimSpace(call.Body))
	if m == nil || c == nil || !c.KnownName(m[1]) {
		return "", false
	}
	return m[1], true
}

// LoadedBefore reports whether calls holds an exact request_tools call loading
// tool that starts before offset.
func LoadedBefore(c *Catalog, calls []Call, tool string, offset int) bool {
	for _, call := range calls {
		if call.Start >= offset {
			continue
		}
		if got, ok := RequestedTool(c, call); ok && got == tool {
			return true
		}
	}
	return false
}

// UnheldCalls lists calls to tools in requestable that offered lacks and that
// no earlier exact request_tools call in the same block loads.
func UnheldCalls(c *Catalog, text string, offered map[string]bool, requestable []string) []Call {
	loadable := map[string]bool{}
	for _, name := range requestable {
		loadable[name] = !offered[name]
	}
	var out []Call
	for _, b := range Blocks(text) {
		calls := Calls(c, b.Text)
		for _, call := range calls {
			if loadable[call.Tool] && !LoadedBefore(c, calls, call.Tool, call.Start) {
				out = append(out, call)
			}
		}
	}
	return out
}
