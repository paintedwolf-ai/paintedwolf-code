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

// Names recognizes a text action envelope naming a tool. Bare JSON must lead
// the response and name an offered tool; explicit <tool_call> and [TOOL_CALLS]
// delimiters are recognized even after leading prose or for deferred tools.
// Trailing prose and argument validity do not matter: recognition requests
// repair, not execution.
func Names(content string, offered []string) []string {
	if len(offered) == 0 {
		return nil
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	var names []string
	collectTaggedEnvelopes(content, &names)
	if len(names) > 0 {
		return names
	}
	if strings.HasPrefix(content, "${") {
		content = content[1:]
	}
	if content == "" || (content[0] != '{' && content[0] != '[') {
		return nil
	}
	decodeJSONNames(content, offered, false, &names)
	return names
}

func collectTaggedEnvelopes(content string, names *[]string) {
	unfenced := stripCodeFences(content)
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

func stripCodeFences(content string) string {
	var b strings.Builder
	inFence := false
	inToolCall := false
	for _, line := range strings.SplitAfter(content, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if !inToolCall && strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if strings.HasPrefix(trimmed, "<tool_call>") && !strings.Contains(trimmed, "</tool_call>") {
			inToolCall = true
		} else if inToolCall && strings.Contains(trimmed, "</tool_call>") {
			inToolCall = false
		}
		b.WriteString(line)
	}
	return b.String()
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
	decoder := json.NewDecoder(io.LimitReader(strings.NewReader(content), 65536))
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
