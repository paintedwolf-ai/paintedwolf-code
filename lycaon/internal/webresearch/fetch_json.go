package webresearch

import (
	"bytes"
	"encoding/json"
	"strings"
)

// expandCompactJSON re-indents a one-line JSON body so text mode can page and
// outline it by line; mode=raw keeps the bytes verbatim. Non-JSON or multi-line
// bodies are returned as is.
func expandCompactJSON(contentType, text string) (string, bool) {
	if !jsonMediaType(contentType) || strings.Count(text, "\n") > 1 {
		return text, false
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return text, false
	}
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(trimmed), "", "  "); err != nil {
		return text, false
	}
	return out.String() + "\n", true
}

func jsonMediaType(contentType string) bool {
	ct := mediaTypeOnly(contentType)
	return ct == "application/json" || strings.HasSuffix(ct, "+json")
}
