package logview

import (
	"encoding/json"
	"fmt"
	"strings"
)

// toolSummary produces a developer-readable one-line summary of a tool call paired
// with its result — "scraper.py · 180 lines", "`pytest` · exit 0", "scan: 7
// findings" — and reports whether it failed. It reads the "what" from the call args
// and the outcome from the result, falling back gracefully for unknown tools.
func toolSummary(name string, args json.RawMessage, result string) (summary string, failed bool) {
	a := parseObj(string(args))
	r := parseObj(stripToolTag(result))

	switch normalizeToolName(name) {
	case "read":
		path := argStr(a, "path")
		if path == "" {
			path = receiptStr(r, "path") // unpaired result still carries the path in its receipt
		}
		s := orDash(path)
		if n := contentLines(r); n > 0 {
			s += dimDot(fmt.Sprintf("%d lines", n))
		}
		return s, false
	case "command":
		cmd := truncate(argStr(a, "command"), 48)
		code := objInt(r, "ExitCode", "exit_code")
		return "`" + cmd + "`" + dimDot(fmt.Sprintf("exit %d", code)), code != 0
	case "grep":
		return quoteArg(a, "pattern", "query") + dimDot(countLabel(r, "matches")), false
	case "find":
		count := anyCountLabel(r, "matches", "entries", "results")
		if p := argStr(a, "pattern", "glob", "query"); p != "" {
			return p + dimDot(count), false
		}
		return orDash(count), false
	case "list_dir":
		path := orDash(argStr(a, "path"))
		if view := strings.TrimSpace(argStr(r, "view")); view == "tags" || view == "digest" || view == "map" {
			return path + dimDot("map"), false
		}
		return path + dimDot(countLabel(r, "entries")), false
	case "stat":
		return orDash(receiptStr(r, "path")), false
	case "write", "edit", "replace_lines", "code_rewrite":
		return "wrote " + orDash(argStr(a, "path")), false
	case "jq_edit":
		return "wrote " + orDash(argStr(a, "dest", "path")), false
	case "update_progress":
		return progressSummary(argStr(a, "content")), false
	case "web_search":
		return quoteArg(a, "query") + dimDot(anyCountLabel(r, "results", "matches")), false
	case "fetch_url":
		return orDash(argStr(a, "url", "query")), false
	case "record_finding":
		return truncate(argStr(a, "summary", "ref"), 70), false
	case "task":
		obj := truncate(argStr(a, "prompt", "objective", "task"), 56)
		return strings.TrimSpace(argStr(a, "agent_type") + ": " + obj), false
	case "scan_list", "scan_summary", "scan_query", "scan_compare", "scan_pack":
		return scanSummary(r), false
	case "pack_board":
		return "board", false
	default:
		if p := receiptStr(r, "path"); p != "" {
			return p, false
		}
		if s := inlineArgs(args); s != "" && s != "{}" {
			return truncate(s, 60), false
		}
		return truncate(oneLine(stripToolTag(result)), 60), false
	}
}

func normalizeToolName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimPrefix(n, "mcp_lycaon_")
	n = strings.TrimPrefix(n, "mcp_")
	return n
}

func parseObj(s string) map[string]any {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") {
		return nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) != nil {
		return nil
	}
	return m
}

// stripToolTag removes a leading "[tool#N]" marker (and any newline) from a result.
func stripToolTag(result string) string {
	r := strings.TrimSpace(result)
	if strings.HasPrefix(r, "[") {
		if i := strings.IndexByte(r, ']'); i >= 0 {
			return strings.TrimSpace(r[i+1:])
		}
	}
	return r
}

func argStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func quoteArg(m map[string]any, keys ...string) string {
	if s := argStr(m, keys...); s != "" {
		return "'" + truncate(s, 40) + "'"
	}
	return "—"
}

func objInt(m map[string]any, keys ...string) int {
	for _, k := range keys {
		if v, ok := m[k].(float64); ok {
			return int(v)
		}
	}
	return 0
}

func arrLen(m map[string]any, key string) (int, bool) {
	if v, ok := m[key].([]any); ok {
		return len(v), true
	}
	return 0, false
}

func countLabel(m map[string]any, key string) string {
	if n, ok := arrLen(m, key); ok {
		return fmt.Sprintf("%d %s", n, key)
	}
	return ""
}

func anyCountLabel(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if n, ok := arrLen(m, k); ok {
			return fmt.Sprintf("%d %s", n, k)
		}
	}
	return ""
}

func dimDot(s string) string {
	if s == "" {
		return ""
	}
	return " · " + s
}

// receiptStr pulls a field out of a result's "receipt" object (tools echo their
// inputs there, so an unpaired result can still name the path it acted on).
func receiptStr(r map[string]any, key string) string {
	if rec, ok := r["receipt"].(map[string]any); ok {
		if v, ok := rec[key].(string); ok {
			return v
		}
	}
	return ""
}

// contentLines counts lines in a read-style result's content field.
func contentLines(r map[string]any) int {
	if c, ok := r["content"].(string); ok && c != "" {
		return strings.Count(c, "\n") + 1
	}
	return 0
}

func scanSummary(r map[string]any) string {
	if n, ok := arrLen(r, "findings"); ok {
		return fmt.Sprintf("%d findings", n)
	}
	if n, ok := arrLen(r, "scans"); ok {
		return fmt.Sprintf("%d scans", n)
	}
	if s := argStr(r, "status"); s != "" {
		return "scan " + s
	}
	return "scan"
}

// progressSummary renders an update_progress checklist as "done/total steps".
func progressSummary(content string) string {
	done, total := 0, 0
	for _, line := range strings.Split(content, "\n") {
		l := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(l, "- [x]") || strings.HasPrefix(l, "- [~]"):
			done++
			total++
		case strings.HasPrefix(l, "- [ ]"):
			total++
		}
	}
	if total == 0 {
		return "progress"
	}
	return fmt.Sprintf("progress %d/%d steps", done, total)
}
