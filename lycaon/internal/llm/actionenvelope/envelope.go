// Package actionenvelope recognizes non-executable text action envelopes.
package actionenvelope

import (
	"encoding/json"
	"io"
	"regexp"
	"slices"
	"strings"
)

var (
	xmlFunctionRE     = regexp.MustCompile(`(?is)(?:^\s*|</function>\s*)<function=([a-zA-Z0-9_-]+)>`)
	inlineXMLToolCall = regexp.MustCompile(`(?is)<tool_call>\s*<function=([a-zA-Z0-9_-]+)>`)
)

// Tool is one offered tool: its name and argument schema on the request.
type Tool struct {
	Name       string
	ArgsSchema map[string]any
}

// Observation lists the tools a response invoked in text instead of calling.
type Observation struct {
	// Tools holds distinct names in discovery order.
	Tools []string
	// Fenced reports that a fence named for a tool carried a call.
	Fenced bool
}

// Detect recognizes tool calls written as text. A fence whose info string
// names a tool counts when its body is that tool's arguments; every other
// fence is prose. <tool_call> and [TOOL_CALLS] delimiters count after leading
// prose and for any tool; bare JSON must lead the response and name an
// offered tool. unoffered names tools the request can reach but does not
// offer. Recognition requests repair, never execution.
func Detect(content string, offered []Tool, unoffered []string) Observation {
	if len(offered) == 0 {
		return Observation{}
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return Observation{}
	}
	schemas := make(map[string]Tool, len(offered))
	names := make([]string, 0, len(offered))
	for _, tool := range offered {
		schemas[tool.Name] = tool
		names = append(names, tool.Name)
	}
	known := append(slices.Clone(names), unoffered...)
	unfenced, fences := splitFences(content, func(name string) bool { return slices.Contains(known, name) })
	var obs Observation
	for _, block := range fences {
		for _, name := range fencedAction(block, schemas, known) {
			appendName(&obs.Tools, name)
			obs.Fenced = true
		}
	}
	collectTaggedEnvelopes(unfenced, &obs.Tools)
	if len(obs.Tools) > 0 {
		return obs
	}
	if strings.HasPrefix(content, "${") {
		content = content[1:]
	}
	if content == "" || (content[0] != '{' && content[0] != '[') {
		return obs
	}
	decodeJSONNames(content, names, false, &obs.Tools)
	return obs
}

func collectTaggedEnvelopes(unfenced string, names *[]string) {
	for _, payload := range extractTaggedPayloads(unfenced) {
		if matches := xmlFunctionRE.FindAllStringSubmatch(payload, -1); len(matches) > 0 {
			for _, m := range matches {
				appendName(names, m[1])
			}
			continue
		}
		decodeJSONNames(payload, nil, true, names)
	}
	for _, m := range inlineXMLToolCall.FindAllStringSubmatch(unfenced, -1) {
		appendName(names, m[1])
	}
}

func extractTaggedPayloads(s string) []string {
	var payloads []string
	for i := 0; i < len(s); {
		if i == 0 || s[i-1] == '\n' {
			j := skipHorizontalSpace(s, i)
			for strings.HasPrefix(s[j:], "<tool_call>") {
				j += len("<tool_call>")
				end := strings.Index(s[j:], "</tool_call>")
				if end < 0 {
					payloads = append(payloads, strings.TrimSpace(s[j:]))
					return payloads
				}
				payloads = append(payloads, strings.TrimSpace(s[j:j+end]))
				j = skipHorizontalSpace(s, j+end+len("</tool_call>"))
			}
			if strings.HasPrefix(s[j:], "[TOOL_CALLS]") {
				payloads = append(payloads, strings.TrimSpace(s[j+len("[TOOL_CALLS]"):]))
				return payloads
			}
		}
		nl := strings.IndexByte(s[i:], '\n')
		if nl < 0 {
			break
		}
		i += nl + 1
	}
	return payloads
}

func skipHorizontalSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

func decodeJSONNames(content string, offered []string, tagged bool, names *[]string) {
	content = strings.TrimSpace(content)
	if content == "" || (content[0] != '{' && content[0] != '[') {
		return
	}
	var value any
	decoder := json.NewDecoder(io.LimitReader(strings.NewReader(content), maxActionBytes))
	if decoder.Decode(&value) != nil {
		return
	}
	collect(value, offered, tagged, names, 0)
}

func collect(value any, offered []string, tagged bool, names *[]string, depth int) {
	if depth > 3 {
		return
	}
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			collect(item, offered, tagged, names, depth+1)
		}
	case map[string]any:
		for _, fields := range [][2]string{{"tool_name", "tool_args"}, {"name", "arguments"}, {"name", "parameters"}, {"name", "args"}, {"tool", "args"}} {
			name, ok := v[fields[0]].(string)
			_, hasArgs := v[fields[1]]
			if ok && hasArgs && (slices.Contains(offered, name) || (tagged && isValidToolName(name))) {
				appendName(names, name)
			}
		}
		// Only declared action containers are traversed, never argument values.
		for _, key := range []string{"tool_calls", "function"} {
			if child, ok := v[key]; ok {
				collect(child, offered, tagged, names, depth+1)
			}
		}
	}
}

func appendName(names *[]string, name string) {
	if isValidToolName(name) && !slices.Contains(*names, name) {
		*names = append(*names, name)
	}
}

func isValidToolName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		isDigit := c >= '0' && c <= '9'
		if i == 0 && !isLetter {
			return false
		}
		if !isLetter && !isDigit && c != '_' && c != '-' {
			return false
		}
	}
	return true
}
