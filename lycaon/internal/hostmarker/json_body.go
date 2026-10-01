package hostmarker

import (
	"encoding/json"
	"strings"
)

// SplitToolJSONBody preserves declared markers around a JSON object.
func SplitToolJSONBody(content string) (prefix, jsonBody, suffix string, ok bool) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", "", "", false
	}

	body := toolBodyAfterPrefix(content)
	if strings.HasPrefix(body, CompactionBannerOpen) {
		body = toolBodyAfterPrefix(compactedToolBody(body))
	}
	if !strings.HasPrefix(body, "{") {
		return "", "", "", false
	}
	prefix = content[:len(content)-len(body)]
	dec := json.NewDecoder(strings.NewReader(body))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return "", "", "", false
	}
	if len(raw) == 0 || raw[0] != '{' {
		return "", "", "", false
	}
	jsonBody = string(raw)

	suffix = body[int(dec.InputOffset()):]
	return prefix, jsonBody, suffix, true
}

func toolBodyAfterPrefix(content string) string {
	_, body := SplitEvidenceHandleTag(content)
	body = strings.TrimSpace(body)
	for _, marker := range []string{OverlayPromoteEventPrefix, OverlayRejectEventPrefix} {
		if strings.HasPrefix(body, marker) {
			return strings.TrimSpace(strings.TrimPrefix(body, marker))
		}
	}
	return body
}

// compactedToolBody accepts pointer residues and explicitly marked excerpts.
func compactedToolBody(body string) string {
	end := strings.Index(body, CompactionBannerClose)
	if end < 0 {
		return ""
	}
	body = strings.TrimSpace(body[end+len(CompactionBannerClose):])
	if strings.HasPrefix(toolBodyAfterPrefix(body), "{") {
		return body
	}
	marker := VerbatimHeadTail + "\n"
	if strings.HasPrefix(body, marker) {
		return strings.TrimSpace(strings.TrimPrefix(body, marker))
	}
	if _, excerpt, found := strings.Cut(body, "\n"+marker); found {
		return strings.TrimSpace(excerpt)
	}
	return body
}
