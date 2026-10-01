// Package jsonfence extracts JSON payloads from LLM markdown output.
package jsonfence

import (
	"encoding/json"
	"regexp"
	"strings"
)

var fenceRE = regexp.MustCompile("(?is)```\\s*(?:json\\s*)?([\\s\\S]*?)```")

// Candidates returns deduped candidate JSON text from raw LLM output: the trimmed
// body, each markdown fence interior, the outermost {...} slice, and the first
// complete JSON value of each (recovers early-closed objects with trailing junk).
func Candidates(content string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		if _, ok := seen[raw]; ok {
			return
		}
		seen[raw] = struct{}{}
		out = append(out, raw)
	}
	addWithFirst := func(raw string) {
		add(raw)
		if first, ok := FirstJSONValue(raw); ok {
			add(first)
		}
	}
	addWithFirst(content)
	for _, match := range fenceRE.FindAllStringSubmatch(content, -1) {
		if len(match) > 1 {
			addWithFirst(match[1])
		}
	}
	if idx := strings.IndexByte(content, '{'); idx >= 0 {
		if end := strings.LastIndexByte(content, '}'); end > idx {
			addWithFirst(content[idx : end+1])
		}
	}
	return out
}

// FirstJSONValue returns the first complete JSON value and ignores trailing bytes.
func FirstJSONValue(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", false
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return "", false
	}
	out := strings.TrimSpace(string(raw))
	if out == "" {
		return "", false
	}
	return out, true
}

// First returns the first ```json fenced payload in content, if any.
func First(content string) (string, bool) {
	match := fenceRE.FindStringSubmatch(content)
	if len(match) < 2 {
		return "", false
	}
	payload := strings.TrimSpace(match[1])
	if payload == "" {
		return "", false
	}
	return payload, true
}

// EnvelopeOnly reports whether content is exclusively one JSON payload — bare JSON
// or a single markdown fence with no surrounding prose. valid parses a candidate;
// callers supply domain-specific decode.
func EnvelopeOnly(content string, valid func(candidate string) bool) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || valid == nil {
		return false
	}
	if valid(trimmed) {
		return true
	}
	match := fenceRE.FindStringSubmatch(trimmed)
	if len(match) < 2 {
		return false
	}
	if strings.TrimSpace(fenceRE.ReplaceAllString(trimmed, "")) != "" {
		return false
	}
	return valid(strings.TrimSpace(match[1]))
}
