package evidence

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/jsonvalue"
)

func populateReadRecord(projectDir string, rec *Record, args map[string]any, content string) {
	path := stringArg(args, "path")
	addRecordPath(projectDir, rec, path, FidelityStructured)
	if _, ok := NormalizeCitationPath(projectDir, path); !ok && strings.TrimSpace(projectDir) != "" {
		return
	}
	if payload, ok := parseReadPayload(content); ok {
		if len(payload.Ranges) > 0 {
			for _, block := range payload.Ranges {
				start := block.Offset
				end := block.EndLine
				if start <= 0 {
					start = 1
				}
				if end < start {
					end = start + CountTextLines(block.Content) - 1
					if end < start {
						end = start
					}
				}
				appendLineRange(rec, start, end)
				if body := strings.TrimSpace(block.Content); body != "" {
					rec.Body = append(rec.Body, body)
				}
			}
			return
		}
		// Outlines and empty symbol reads return no file text, so they cover no lines.
		if payload.Mode == "outline" || (payload.Mode == "symbol" && strings.TrimSpace(payload.Content) == "") {
			return
		}
		start := payload.Offset
		if payload.StartLine > 0 {
			start = payload.StartLine
		}
		end := payload.EndLine
		if start <= 0 {
			start = 1
		}
		if end < start {
			end = start + CountTextLines(payload.Content) - 1
			if end < start {
				end = start
			}
		}
		appendLineRange(rec, start, end)
		if body := strings.TrimSpace(payload.Content); body != "" {
			rec.Body = append(rec.Body, body)
		}
		return
	}
	body := stripToolHostSuffix(content)
	offset := intArg(args, "offset", 1)
	if offset <= 0 {
		offset = 1
	}
	limit := intArg(args, "limit", 0)
	lines := CountTextLines(body)
	end := offset + lines - 1
	if lines == 0 {
		end = offset
	}
	if limit > 0 && lines > limit {
		end = offset + limit - 1
	}
	appendLineRange(rec, offset, end)
	if strings.TrimSpace(body) != "" {
		rec.Body = append(rec.Body, body)
	}
}

// populateSummarizeRecord indexes the summarize response's anchors (path/line/
// excerpt) so a citation of a summarize#N span resolves verbatim.
func populateSummarizeRecord(projectDir string, rec *Record, content string) {
	for _, h := range summarizeAnchorDetails(content) {
		addRecordPath(projectDir, rec, h.path, FidelityStructured)
		rel, ok := NormalizeCitationPath(projectDir, h.path)
		if !ok {
			if strings.TrimSpace(projectDir) != "" {
				continue
			}
			rel = NormalizeLedgerPath(h.path)
		}
		if rel == "" || h.line <= 0 {
			continue
		}
		appendLineRange(rec, h.line, h.line)
		rec.addGrepLine(rel, h.line, h.content)
	}
}

func summarizeAnchorDetails(content string) []grepMatchDetail {
	obj, ok := jsonToolPayload(content)
	if !ok {
		return nil
	}
	raw, ok := obj["anchors"].([]any)
	if !ok {
		return nil
	}
	var out []grepMatchDetail
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		path, _ := m["path"].(string)
		line := jsonvalue.Int(m["line"])
		excerpt, _ := m["excerpt"].(string)
		path = NormalizeLedgerPath(path)
		if path == "" || line <= 0 {
			continue
		}
		out = append(out, grepMatchDetail{path: path, line: line, content: excerpt})
	}
	return out
}

func appendLineRange(rec *Record, start, end int) {
	if rec == nil || start <= 0 || end <= 0 || end < start {
		return
	}
	rec.LineRanges = append(rec.LineRanges, LineRange{Start: start, End: end})
}

func populateGrepRecord(projectDir string, rec *Record, args map[string]any, content string) {
	addRecordPath(projectDir, rec, stringArg(args, "path"), FidelityStructured)
	for _, p := range grepMatchPaths(content) {
		addRecordPath(projectDir, rec, p, FidelityStructured)
	}
	for _, m := range grepMatchDetails(content) {
		addRecordPath(projectDir, rec, m.path, FidelityStructured)
		rel, ok := NormalizeCitationPath(projectDir, m.path)
		if !ok {
			if strings.TrimSpace(projectDir) != "" {
				continue
			}
			rel = NormalizeLedgerPath(m.path)
		}
		if rel == "" || m.line <= 0 {
			continue
		}
		b := len(m.contextBefore)
		for i, lineContent := range m.contextBefore {
			rec.addGrepLine(rel, m.line-b+i, lineContent)
		}
		rec.addGrepLine(rel, m.line, m.content)
		for i, lineContent := range m.contextAfter {
			rec.addGrepLine(rel, m.line+1+i, lineContent)
		}
		appendLineRange(rec, m.line-b, m.line+len(m.contextAfter))
	}
	for _, h := range highlightDetails(content) {
		addRecordPath(projectDir, rec, h.path, FidelityStructured)
		rel, ok := NormalizeCitationPath(projectDir, h.path)
		if !ok {
			if strings.TrimSpace(projectDir) != "" {
				continue
			}
			rel = NormalizeLedgerPath(h.path)
		}
		if rel == "" || h.line <= 0 {
			continue
		}
		appendLineRange(rec, h.line, h.line)
		rec.addGrepLine(rel, h.line, h.content)
	}
}

type grepMatchDetail struct {
	path          string
	line          int
	content       string
	contextBefore []string
	contextAfter  []string
}

type readToolPayload struct {
	Path    string `json:"path"`
	Mode    string `json:"mode"`
	Content string `json:"content"`
	Offset  int    `json:"offset"`
	// Symbol reads provide an explicit line span.
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
	Limit     int `json:"limit"`
	Ranges    []struct {
		Offset  int    `json:"offset"`
		EndLine int    `json:"end_line"`
		Content string `json:"content"`
	} `json:"ranges"`
}

func parseReadPayload(content string) (readToolPayload, bool) {
	obj, ok := jsonToolPayload(content)
	if !ok {
		return readToolPayload{}, false
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return readToolPayload{}, false
	}
	var payload readToolPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return readToolPayload{}, false
	}
	if strings.TrimSpace(payload.Path) == "" && strings.TrimSpace(payload.Content) == "" {
		return readToolPayload{}, false
	}
	return payload, true
}

func grepMatchDetails(content string) []grepMatchDetail {
	obj, ok := jsonToolPayload(content)
	if !ok {
		return nil
	}
	raw, ok := obj["matches"].([]any)
	if !ok {
		return nil
	}
	var out []grepMatchDetail
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		path, _ := m["path"].(string)
		line := jsonvalue.Int(m["line"])
		matchContent, _ := m["content"].(string)
		path = NormalizeLedgerPath(path)
		if path == "" || line <= 0 {
			continue
		}
		var contextBefore []string
		if beforeRaw, ok := m["context_before"].([]any); ok {
			for _, item := range beforeRaw {
				if s, ok := item.(string); ok {
					contextBefore = append(contextBefore, s)
				}
			}
		}
		var contextAfter []string
		if afterRaw, ok := m["context_after"].([]any); ok {
			for _, item := range afterRaw {
				if s, ok := item.(string); ok {
					contextAfter = append(contextAfter, s)
				}
			}
		}
		out = append(out, grepMatchDetail{
			path:          path,
			line:          line,
			content:       matchContent,
			contextBefore: contextBefore,
			contextAfter:  contextAfter,
		})
	}
	return out
}

func highlightDetails(content string) []grepMatchDetail {
	obj, ok := jsonToolPayload(content)
	if !ok {
		return nil
	}
	raw, ok := obj["highlights"].([]any)
	if !ok {
		return nil
	}
	var out []grepMatchDetail
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		path, _ := m["path"].(string)
		line := jsonvalue.Int(m["line"])
		matchContent, _ := m["content"].(string)
		path = NormalizeLedgerPath(path)
		if path == "" || line <= 0 {
			continue
		}
		out = append(out, grepMatchDetail{
			path:    path,
			line:    line,
			content: matchContent,
		})
	}
	return out
}

func grepMatchPaths(content string) []string {
	obj, ok := jsonToolPayload(content)
	if !ok {
		return nil
	}
	raw, ok := obj["matches"].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if p, ok := m["path"].(string); ok && strings.TrimSpace(p) != "" {
			out = append(out, NormalizeLedgerPath(p))
		}
	}
	return out
}
