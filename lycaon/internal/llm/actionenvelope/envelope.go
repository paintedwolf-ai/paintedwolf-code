// Package actionenvelope recognizes non-executable text action envelopes.
package actionenvelope

import (
	"encoding/json"
	"io"
	"slices"
	"strings"
)

// Names recognizes a leading JSON action naming an offered tool. Trailing prose
// and argument validity do not matter: recognition requests repair, not execution.
func Names(content string, offered []string) []string {
	if len(offered) == 0 {
		return nil
	}
	content = strings.TrimSpace(content)
	if content == "" || (content[0] != '{' && content[0] != '[') {
		return nil
	}
	var value any
	decoder := json.NewDecoder(io.LimitReader(strings.NewReader(content), 65536))
	if decoder.Decode(&value) != nil {
		return nil
	}
	var names []string
	collect(value, offered, &names, 0)
	return names
}

func collect(value any, offered []string, names *[]string, depth int) {
	if depth > 3 {
		return
	}
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			collect(item, offered, names, depth+1)
		}
	case map[string]any:
		for _, fields := range [][2]string{{"tool_name", "tool_args"}, {"name", "arguments"}, {"name", "parameters"}, {"name", "args"}, {"tool", "args"}} {
			name, ok := v[fields[0]].(string)
			_, hasArgs := v[fields[1]]
			if ok && hasArgs && slices.Contains(offered, name) && !slices.Contains(*names, name) {
				*names = append(*names, name)
			}
		}
		// Only declared action containers are traversed, never argument values.
		for _, key := range []string{"tool_calls", "function"} {
			if child, ok := v[key]; ok {
				collect(child, offered, names, depth+1)
			}
		}
	}
}
