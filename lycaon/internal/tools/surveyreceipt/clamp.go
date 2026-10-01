package surveyreceipt

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

const surveyByteClampHintCode = "TOOL_SURVEY_BYTE_CLAMPED"

var paginatedPageKeys = []string{"results", "matches", "entries", "tags"}

// SessionClamp is a byte-capped survey tool payload plus template vars for TOOL_SURVEY_BYTE_CLAMPED.
type SessionClamp struct {
	Output string
	Vars   map[string]any
	// KeptEntries is how many leading entries of a paginated page remained; nil for content reads.
	KeptEntries *int
	// KeptThroughLine is the last numbered line a content read kept; zero when unknown.
	KeptThroughLine int
}

// ClampSessionToolOutput trims survey JSON to maxBytes while preserving validity.
func ClampSessionToolOutput(content string, maxBytes int) (SessionClamp, bool) {
	content = strings.TrimSpace(content)
	if maxBytes <= 0 || len(content) <= maxBytes {
		return SessionClamp{Output: content}, false
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(content), &obj); err != nil {
		return SessionClamp{Output: content}, false
	}
	pageKey, page, ok := paginatedPage(obj)
	if !ok || len(page) == 0 {
		out, endLine, applied := clampReadContent(obj, maxBytes)
		if !applied {
			return SessionClamp{Output: content}, false
		}
		return SessionClamp{Output: out, Vars: hintVarsFromObject(obj), KeptThroughLine: endLine}, true
	}
	originalLen := len(page)
	offset := jsonvalue.Int(obj["offset"])
	for {
		obj[pageKey] = page
		if len(page) < originalLen {
			markPaginatedTruncated(obj, pageKey, offset, len(page), originalLen)
		}
		out := marshalReceiptBytes(obj)
		if len(out) <= maxBytes {
			kept := len(page)
			return SessionClamp{Output: string(out), Vars: hintVarsFromObject(obj), KeptEntries: &kept}, true
		}
		if len(page) == 0 {
			break
		}
		page = page[:len(page)-1]
	}
	return SessionClamp{Output: content}, false
}

func paginatedPage(obj map[string]any) (string, []any, bool) {
	for _, key := range paginatedPageKeys {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		page, ok := raw.([]any)
		if !ok {
			continue
		}
		return key, page, true
	}
	return "", nil, false
}

func markPaginatedTruncated(obj map[string]any, pageKey string, offset, kept, originalLen int) {
	if kept >= originalLen {
		return
	}
	obj["truncated"] = true
	next := offset + kept
	obj["next_offset"] = next
	prev, _ := obj["truncation_banner"].(string)
	notice := surveyByteClampHintCode
	if strings.TrimSpace(prev) == "" {
		obj["truncation_banner"] = notice
	} else if !strings.Contains(prev, notice) {
		obj["truncation_banner"] = prev + "\n" + notice
	}
	if receiptRaw, ok := obj["receipt"].(map[string]any); ok {
		receiptRaw["truncated"] = true
	}
}

// marshalReceiptBytes includes the receipt's own decimal byte count.
func marshalReceiptBytes(obj map[string]any) []byte {
	receipt, ok := obj["receipt"].(map[string]any)
	if !ok {
		return mustMarshal(obj)
	}
	receipt["bytes_returned"] = 0
	for {
		out := mustMarshal(obj)
		if jsonvalue.Int(receipt["bytes_returned"]) == len(out) {
			return out
		}
		receipt["bytes_returned"] = len(out)
	}
}

func clampReadContent(obj map[string]any, maxBytes int) (string, int, bool) {
	content, ok := obj["content"].(string)
	if !ok || content == "" {
		return "", 0, false
	}
	receiptOverhead := len(mustMarshal(obj)) - len(content)
	budget := maxBytes - receiptOverhead - 64
	if budget <= 0 {
		return "", 0, false
	}
	if len(content) <= budget {
		return "", 0, false
	}
	trimmed, endLine, ok := trimReadContentLines(content, budget)
	if !ok {
		return "", 0, false
	}
	obj["content"] = trimmed
	obj["truncated"] = true
	if endLine > 0 {
		obj["end_line"] = endLine
		obj["next_offset"] = endLine + 1
	} else {
		delete(obj, "end_line")
		delete(obj, "next_offset")
	}
	if receiptRaw, ok := obj["receipt"].(map[string]any); ok {
		receiptRaw["truncated"] = true
	}
	banner := surveyByteClampHintCode
	if prev, _ := obj["truncation_banner"].(string); strings.TrimSpace(prev) != "" {
		banner = prev + "\n" + banner
	}
	obj["truncation_banner"] = banner
	out := marshalReceiptBytes(obj)
	if len(out) <= maxBytes {
		return string(out), endLine, true
	}
	return "", 0, false
}

// trimReadContentLines keeps whole numbered read lines (N\ttext) within budget.
func trimReadContentLines(content string, budget int) (trimmed string, endLine int, ok bool) {
	if budget <= 0 || len(content) <= budget {
		return "", 0, false
	}
	lines := strings.Split(content, "\n")
	var b strings.Builder
	for _, line := range lines {
		if line == "" && b.Len() == 0 {
			continue
		}
		chunk := line
		if b.Len() > 0 {
			chunk = "\n" + line
		}
		if b.Len()+len(chunk) > budget {
			break
		}
		b.WriteString(chunk)
		if tab := strings.IndexByte(line, '\t'); tab > 0 {
			if n, err := strconv.Atoi(line[:tab]); err == nil && n > 0 {
				endLine = n
			}
		}
	}
	if b.Len() == 0 {
		return "", 0, false
	}
	return b.String(), endLine, true
}

func hintVarsFromObject(obj map[string]any) map[string]any {
	offset := jsonvalue.Int(obj["offset"])
	next := jsonvalue.Int(obj["next_offset"])
	if next <= offset {
		next = 0
	}
	if pageKey, page, ok := paginatedPage(obj); ok {
		return map[string]any{
			"kept": len(page), "kept_unit": "entries", "page_key": pageKey, "offset": offset, "next_offset": next,
		}
	}
	content, _ := obj["content"].(string)
	return map[string]any{
		"kept": len(content), "kept_unit": "bytes", "page_key": "content", "offset": offset, "next_offset": next,
	}
}

func mustMarshal(v any) []byte {
	out, err := surveyjson.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return out
}
