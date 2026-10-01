package search

import (
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"strings"
)

// ProjectHitDisplay projects a scannable title and context.
func ProjectHitDisplay(hit *Hit) {
	if hit == nil {
		return
	}
	if strings.TrimSpace(hit.Title) != "" {
		return
	}
	title, context := hitDisplay(hit)
	hit.Title = title
	hit.Context = context
}

func hitDisplay(hit *Hit) (title, context string) {
	kind := strings.ToLower(strings.TrimSpace(hit.HitKind))
	snippet := strings.TrimSpace(hit.Snippet)
	pathLabel := pathLineLabel(hit.Path, hit.Line)

	switch kind {
	case "file":
		// The folder disambiguates files with the same name.
		path := strings.TrimSpace(hit.Path)
		if path == "" {
			return "File", ""
		}
		base := path
		dir := ""
		if idx := strings.LastIndex(path, "/"); idx >= 0 {
			base = path[idx+1:]
			dir = path[:idx]
		}
		return base, dir
	case "code":
		if snippet != "" {
			return softTitle(snippet, 220), emptyIf(pathLabel, pathLabel != snippet)
		}
		if pathLabel != "" {
			return pathLabel, ""
		}
		return "Code match", ""
	case "tool":
		// Tool-call snippets repeat the tool name before their arguments.
		if name := strings.TrimSpace(hit.Tool); name != "" {
			body := strings.TrimSpace(strings.TrimPrefix(snippet, name))
			return name, firstNonEmpty(readableSummary(body, 80), pathLabel, nonUUIDRef(hit.SourceRef))
		}
		if summary := readableSummary(snippet, 220); summary != "" {
			return summary, nonUUIDRef(hit.SourceRef)
		}
		if snippet != "" {
			return "Tool result", nonUUIDRef(hit.SourceRef)
		}
		return "Tool", nonUUIDRef(hit.SourceRef)
	default:
		context := firstNonEmpty(pathLabel, strings.TrimSpace(hit.URL), nonUUIDRef(hit.SourceRef))
		if title := structuredTitle(snippet); title != "" {
			return title, context
		}
		if summary := readableSummary(snippet, 220); summary != "" {
			return summary, context
		}
		if pathLabel != "" {
			return pathLabel, ""
		}
		if ref := strings.TrimSpace(hit.SourceRef); ref != "" && !looksLikeUUID(ref) {
			return softTitle(ref, 220), ""
		}
		if kind != "" {
			return strings.ToUpper(kind[:1]) + kind[1:], ""
		}
		return "Hit", ""
	}
}

func structuredTitle(text string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return ""
	}
	for _, key := range []string{"caption", "title", "summary", "name", "message", "rule_id", "path", "content"} {
		value, ok := payload[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		return softTitle(firstLine(value), 220)
	}
	return ""
}

func pathLineLabel(path string, line int) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if line > 0 {
		return fmt.Sprintf("%s:%d", path, line)
	}
	return path
}

// readableSummary excludes structured payloads from row labels.
func readableSummary(text string, max int) string {
	line := firstLine(text)
	if line == "" || line[0] == '{' || line[0] == '[' {
		return ""
	}
	return softTitle(line, max)
}

func softTitle(text string, max int) string {
	t := strings.Join(strings.Fields(text), " ")
	if max <= 0 {
		return t
	}
	return runeclamp.Clamp(t, max)
}

func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return strings.TrimSpace(text)
}

func nonUUIDRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || looksLikeUUID(ref) {
		return ""
	}
	return ref
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

func emptyIf(s string, ok bool) string {
	if ok {
		return s
	}
	return ""
}
