// Package actionenvelope recognizes the reserved, non-executable text action grammar.
package actionenvelope

import (
	"encoding/json"
	"io"
	"slices"
	"strings"
)

// Names returns offered tool names only when the entire response is a reserved
// envelope or batch. Arguments are never returned to an executor.
func Names(content string, offered []string) []string {
	if len(offered) == 0 || len(content) > 16384 {
		return nil
	}
	content = strings.TrimSpace(content)
	if content == "" || (content[0] != '{' && content[0] != '[') {
		return nil
	}
	type envelope struct {
		Name string                     `json:"tool_name"`
		Args map[string]json.RawMessage `json:"tool_args"`
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	var batch []envelope
	if content[0] == '[' {
		if decoder.Decode(&batch) != nil {
			return nil
		}
	} else {
		var item envelope
		if decoder.Decode(&item) != nil {
			return nil
		}
		batch = []envelope{item}
	}
	if len(batch) == 0 || len(batch) > 16 || decoder.Decode(new(any)) != io.EOF {
		return nil
	}
	var names []string
	for _, item := range batch {
		if item.Args == nil || !slices.Contains(offered, item.Name) {
			return nil
		}
		if !slices.Contains(names, item.Name) {
			names = append(names, item.Name)
		}
	}
	return names
}
