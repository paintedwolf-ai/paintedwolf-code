package search

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/jsonvalue"
)

type toolHandleSpec struct {
	handle  string
	path    string
	line    int
	snippet string
}

// extractToolHandleSpecs collects minted handles from message metadata, leading
// handle tags, and embedded child-handle arrays in authenticated tool JSON.
func extractToolHandleSpecs(msgHandles []string, defaultPath, defaultSnippet, content string, allowEmbedded bool) []toolHandleSpec {
	var specs []toolHandleSpec
	idxByHandle := make(map[string]int, len(msgHandles)+4)
	upsert := func(handle, itemPath string, itemLine int, itemSnippet string) {
		handle = strings.TrimSpace(handle)
		if handle == "" {
			return
		}
		itemPath = firstNonEmpty(cleanToolPath(itemPath), defaultPath)
		itemSnippet = firstNonEmpty(truncateSnippet(itemSnippet, 2000), defaultSnippet)
		if i, exists := idxByHandle[handle]; exists {
			if specs[i].path == "" && itemPath != "" {
				specs[i].path = itemPath
			}
			if specs[i].line <= 0 && itemLine > 0 {
				specs[i].line = itemLine
			}
			if specs[i].snippet == defaultSnippet && itemSnippet != defaultSnippet {
				specs[i].snippet = itemSnippet
			}
			return
		}
		idxByHandle[handle] = len(specs)
		specs = append(specs, toolHandleSpec{
			handle:  handle,
			path:    itemPath,
			line:    itemLine,
			snippet: itemSnippet,
		})
	}
	for _, h := range msgHandles {
		upsert(h, "", 0, "")
	}
	if allowEmbedded && len(msgHandles) > 0 {
		allowed := make(map[string]bool)
		for _, h := range evidence.ExtractEmbeddedHandles(content, msgHandles...) {
			allowed[h] = true
		}
		if _, jsonBody, _, ok := hostmarker.SplitToolJSONBody(content); ok {
			var obj map[string]any
			if json.Unmarshal([]byte(jsonBody), &obj) == nil {
				collectEmbeddedJSONHandleSpecs(obj, func(handle, path string, line int, snippet string) {
					if allowed[handle] {
						upsert(handle, path, line, snippet)
					}
				})
			}
		}
	}
	if len(specs) == 0 {
		specs = append(specs, toolHandleSpec{
			path:    defaultPath,
			snippet: defaultSnippet,
		})
	}
	return specs
}

func collectEmbeddedJSONHandleSpecs(obj map[string]any, upsert func(handle, path string, line int, snippet string)) {
	if anchors, ok := obj["anchors"].([]any); ok {
		for _, item := range anchors {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if h := stringField(m, "handle"); evidence.IsEvidenceHandleToken(h) {
				upsert(h, stringField(m, "path"), jsonvalue.Int(m["line"]), stringField(m, "excerpt"))
			}
		}
	}
	if highlights, ok := obj["highlights"].([]any); ok {
		for _, item := range highlights {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if h := stringField(m, "handle"); evidence.IsEvidenceHandleToken(h) {
				upsert(h, stringField(m, "path"), jsonvalue.Int(m["line"]), firstNonEmpty(stringField(m, "content"), stringField(m, "excerpt")))
			}
		}
	}
	if pack, ok := obj["pack"].(map[string]any); ok {
		if windows, ok := pack["substance"].([]any); ok {
			for _, item := range windows {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if h := stringField(m, "handle"); evidence.IsEvidenceHandleToken(h) {
					upsert(h, stringField(m, "path"), jsonvalue.Int(m["start_line"]), stringField(m, "body"))
				}
			}
		}
	}
	if frames, ok := obj["frames"].([]any); ok {
		for _, item := range frames {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			h := firstNonEmpty(stringField(m, "evidence_handle"), stringField(m, "handle"))
			if evidence.IsEvidenceHandleToken(h) {
				upsert(h, "", 0, stringField(m, "caption"))
			}
		}
	}
	if rawHandles, ok := obj["highlight_handles"].([]any); ok {
		for _, item := range rawHandles {
			if h, ok := item.(string); ok && evidence.IsEvidenceHandleToken(h) {
				upsert(h, "", 0, "")
			}
		}
	}
}

func stringField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}
