package actionenvelope

import (
	"encoding/json"
	"io"
	"strings"
)

const maxActionBytes = 65536

// fence is one fenced block whose info string names a tool.
type fence struct {
	tool string
	body string
}

// splitFences returns the text outside every fenced block and the blocks
// whose first info token satisfies isTool. A fence line inside an open
// <tool_call> payload is payload text.
func splitFences(content string, isTool func(string) bool) (string, []fence) {
	var (
		unfenced   strings.Builder
		body       strings.Builder
		fences     []fence
		open       bool
		marker     byte
		run        int
		tool       string
		inToolCall bool
	)
	flush := func() {
		if tool != "" {
			fences = append(fences, fence{tool: tool, body: body.String()})
		}
		body.Reset()
		open, tool = false, ""
	}
	for _, line := range strings.SplitAfter(content, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if open {
			if char, length, rest := fenceRun(trimmed); char == marker && length >= run && strings.TrimSpace(rest) == "" {
				flush()
				continue
			}
			body.WriteString(line)
			continue
		}
		if !inToolCall {
			if char, length, rest := fenceRun(trimmed); length >= 3 && (char == '~' || !strings.Contains(rest, "`")) {
				open, marker, run = true, char, length
				if fields := strings.Fields(rest); len(fields) > 0 && isTool(fields[0]) {
					tool = fields[0]
				}
				continue
			}
		}
		if strings.HasPrefix(trimmed, "<tool_call>") && !strings.Contains(trimmed, "</tool_call>") {
			inToolCall = true
		} else if inToolCall && strings.Contains(trimmed, "</tool_call>") {
			inToolCall = false
		}
		unfenced.WriteString(line)
	}
	if open {
		flush()
	}
	return unfenced.String(), fences
}

// fenceRun measures a leading run of backticks or tildes.
func fenceRun(line string) (char byte, length int, rest string) {
	if line == "" || (line[0] != '`' && line[0] != '~') {
		return 0, 0, line
	}
	char = line[0]
	for length < len(line) && line[length] == char {
		length++
	}
	return char, length, line[length:]
}

// fencedAction names the tools a tool-named fence invokes. An offered tool's
// body must fit its argument schema; an unoffered tool has no schema on the
// request, so any non-empty argument object counts.
func fencedAction(block fence, offered map[string]Tool, known []string) []string {
	body := strings.TrimSpace(block.body)
	if body == "" || len(body) > maxActionBytes || (body[0] != '{' && body[0] != '[') {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(body))
	var value any
	if decoder.Decode(&value) != nil {
		return nil
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil
	}
	var wrapped []string
	collect(value, known, false, &wrapped, 0)
	if len(wrapped) > 0 {
		return wrapped
	}
	fits := func(args map[string]any) bool { return len(args) > 0 }
	if tool, ok := offered[block.tool]; ok {
		fits = tool.accepts
	}
	switch v := value.(type) {
	case map[string]any:
		if fits(v) {
			return []string{block.tool}
		}
	case []any:
		if len(v) == 0 {
			return nil
		}
		for _, item := range v {
			args, ok := item.(map[string]any)
			if !ok || !fits(args) {
				return nil
			}
		}
		return []string{block.tool}
	}
	return nil
}

// accepts reports whether args could be this tool's arguments: every key is a
// declared property, and an empty object only when nothing is required.
func (t Tool) accepts(args map[string]any) bool {
	properties, _ := t.ArgsSchema["properties"].(map[string]any)
	if len(properties) == 0 {
		return len(args) > 0
	}
	for key := range args {
		if _, declared := properties[key]; !declared {
			return false
		}
	}
	if len(args) > 0 {
		return true
	}
	switch required := t.ArgsSchema["required"].(type) {
	case []any:
		return len(required) == 0
	case []string:
		return len(required) == 0
	}
	return true
}
