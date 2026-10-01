package logview

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// shortID truncates a UUID to its first segment for compact, still-recognizable rows.
func shortID(id string) string {
	if id == "" {
		return "—"
	}
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// scopeDescriptor extracts a compact task mode and focus tag.
func scopeDescriptor(task string) string {
	mode := ""
	var paths []string
	inPaths := false
	for _, raw := range strings.Split(task, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "- mode:"):
			mode = strings.TrimSpace(strings.TrimPrefix(line, "- mode:"))
		case strings.HasPrefix(line, "suggested paths:") || line == "- suggested paths:":
			inPaths = true
		case inPaths && strings.HasPrefix(line, "- ") && !strings.Contains(line, ":"):
			paths = append(paths, strings.TrimSpace(line[2:]))
		case inPaths:
			inPaths = false // a blank line or a new "key:" ends the path list
		}
	}
	if mode == "" {
		return ""
	}
	if len(paths) == 0 {
		return mode
	}
	return mode + ": " + strings.Join(paths, ", ")
}

// wrapPlain soft-wraps a plain-text line to width columns, breaking at the last
// space within the budget and hard-breaking tokens longer than the width.
func wrapPlain(line string, width int) []string {
	if width <= 1 {
		return []string{line}
	}
	r := []rune(line)
	if len(r) <= width {
		return []string{line}
	}
	var out []string
	for len(r) > width {
		brk := -1
		for i := width; i >= 1; i-- {
			if r[i] == ' ' || r[i] == '\t' {
				brk = i
				break
			}
		}
		if brk <= 0 {
			brk = width // hard break mid-token
		}
		out = append(out, strings.TrimRight(string(r[:brk]), " \t"))
		j := brk
		for j < len(r) && (r[j] == ' ' || r[j] == '\t') {
			j++
		}
		r = r[j:]
	}
	if len(r) > 0 {
		out = append(out, string(r))
	}
	return out
}

func dur(ms *int) string {
	if ms == nil {
		return "—"
	}
	return strconv.Itoa(*ms) + "ms"
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// messageText renders a captured message Content (JSON string or content-block
// array) as plain text. Arrays are flattened to their text parts; anything
// unrecognized falls back to compact JSON so nothing is silently dropped.
func messageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil && len(blocks) > 0 {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return string(raw)
}

// prettyJSON indents compact JSON for readable tool args and results, returning the
// input unchanged when it is not valid JSON.
func prettyJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}
