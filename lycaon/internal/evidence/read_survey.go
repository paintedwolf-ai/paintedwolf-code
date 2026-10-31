package evidence

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
)

type readWireHighlight struct {
	Handle  string `json:"handle,omitempty"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

type readWireResponse struct {
	Mode       string              `json:"mode"`
	Highlights []readWireHighlight `json:"highlights"`
}

// ReadEvidenceSurvey decides survey-grade capture for a read tool result.
func ReadEvidenceSurvey(args map[string]any, content string) bool {
	if readArgsExpressedScope(args) {
		return false
	}
	var wire readWireResponse
	if err := json.Unmarshal([]byte(stripToolHostSuffix(content)), &wire); err != nil {
		return ActiveBinding().IsSurveyKind("read")
	}
	if strings.EqualFold(strings.TrimSpace(wire.Mode), "outline") {
		return true
	}
	return false
}

func readArgsExpressedScope(args map[string]any) bool {
	if strings.TrimSpace(stringArg(args, "symbol")) != "" {
		return true
	}
	if raw, ok := args["ranges"].([]any); ok && len(raw) > 0 {
		return true
	}
	if _, ok := args["offset"]; ok {
		return true
	}
	if _, ok := args["limit"]; ok {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(stringArg(args, "mode")), "content") {
		return true
	}
	return false
}

// ReadHighlightRecords builds non-survey highlight records from a read JSON payload.
func ReadHighlightRecords(projectDir string, content string) []Record {
	return highlightRecords(projectDir, "read", content)
}

func highlightRecords(projectDir, sourceTool, content string) []Record {
	var wire readWireResponse
	if err := json.Unmarshal([]byte(stripToolHostSuffix(content)), &wire); err != nil {
		return nil
	}
	if len(wire.Highlights) == 0 {
		return nil
	}
	binding := ActiveBinding()
	var out []Record
	for _, h := range wire.Highlights {
		path := strings.TrimSpace(h.Path)
		excerpt := strings.TrimSpace(h.Content)
		if path == "" || h.Line <= 0 || excerpt == "" {
			continue
		}
		kind := binding.ToolKind(sourceTool)
		rec := Record{
			Kind:       kind,
			Shape:      binding.ShapeForToolKind(sourceTool, kind),
			SourceTool: sourceTool,
			Survey:     false,
			Path:       path,
			LineRanges: []LineRange{{Start: h.Line, End: h.Line}},
			Body:       []string{excerpt},
			Fidelity:   FidelityStructured,
		}
		if norm, ok := NormalizeCitationPath(projectDir, path); ok {
			rec.Path = norm
		}
		out = append(out, rec)
	}
	return out
}

// PatchReadHighlightHandles rewrites read JSON with minted highlight handles.
// Host feedback banners (>>> Tool feedback…) and handle prefixes are preserved.
func PatchReadHighlightHandles(content string, handles []string) (string, error) {
	prefix, body, suffix := splitToolPayloadEnvelope(content)
	var wire map[string]any
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		return content, err
	}
	raw, ok := wire["highlights"].([]any)
	if !ok || len(raw) == 0 {
		return content, nil
	}
	hi := 0
	for i, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if hi >= len(handles) {
			break
		}
		if strings.TrimSpace(handles[hi]) == "" {
			continue
		}
		m["handle"] = handles[hi]
		raw[i] = m
		hi++
	}
	wire["highlights"] = raw
	out, err := json.Marshal(wire)
	if err != nil {
		return content, err
	}
	return prefix + string(out) + suffix, nil
}

// ListHighlightRecords builds non-survey highlight records from list_dir JSON.
func ListHighlightRecords(projectDir string, content string) []Record {
	return highlightRecords(projectDir, "list_dir", content)
}

// GrepHighlightRecords builds non-survey highlight records from grep JSON.
func GrepHighlightRecords(projectDir string, content string) []Record {
	return highlightRecords(projectDir, "grep", content)
}

// FindHighlightRecords builds non-survey highlight records from find JSON.
func FindHighlightRecords(projectDir string, content string) []Record {
	return highlightRecords(projectDir, "find", content)
}

type summarizeWireAnchor struct {
	Handle  string `json:"handle,omitempty"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Excerpt string `json:"excerpt"`
}

type summarizeWireResponse struct {
	Anchors []summarizeWireAnchor `json:"anchors"`
	Pack    summarizePackWire     `json:"pack"`
}

// SummarizeAnchorHighlightRecords builds one evidence record per summarize anchor,
// or from pack material when anchors[] is empty.
func SummarizeAnchorHighlightRecords(projectDir, content string) []Record {
	obj, ok := jsonToolPayload(content)
	if !ok {
		return nil
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil
	}
	var wire summarizeWireResponse
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil
	}
	if len(wire.Anchors) == 0 {
		return SummarizePackHighlightRecords(projectDir, content)
	}
	binding := ActiveBinding()
	var out []Record
	for _, a := range wire.Anchors {
		path := strings.TrimSpace(a.Path)
		excerpt := strings.TrimSpace(a.Excerpt)
		if path == "" || a.Line <= 0 || excerpt == "" {
			continue
		}
		kind := binding.ToolKind("summarize")
		rec := Record{
			Kind:       kind,
			Shape:      binding.ShapeForToolKind("summarize", kind),
			SourceTool: "summarize",
			Survey:     false,
			Path:       path,
			LineRanges: []LineRange{{Start: a.Line, End: a.Line}},
			Body:       []string{excerpt},
			Fidelity:   FidelityStructured,
		}
		rel := path
		if norm, ok := NormalizeCitationPath(projectDir, path); ok {
			rec.Path = norm
			rel = norm
		}
		rec.addGrepLine(rel, a.Line, excerpt)
		for _, window := range wire.Pack.Substance {
			if window.Path != a.Path || (a.Line < window.StartLine || a.Line > window.EndLine) {
				continue
			}
			rec.LineRanges = []LineRange{{Start: window.StartLine, End: max(window.StartLine, window.EndLine)}}
			for _, line := range substanceBodyLines(window.Body) {
				if line.line < window.StartLine || line.line > window.EndLine {
					continue
				}
				rec.addGrepLine(rel, line.line, line.content)
				rec.Body = append(rec.Body, line.content)
			}
		}
		out = append(out, rec)
	}
	return out
}

// PatchSummarizeAnchorHandles rewrites summarize JSON with minted anchor handles.
func PatchSummarizeAnchorHandles(content string, handles []string) (string, error) {
	prefix, body, suffix := splitToolPayloadEnvelope(content)
	obj, ok := jsonToolPayload(body)
	if !ok {
		return content, nil
	}
	rawAnchors, _ := obj["anchors"].([]any)
	hi := 0
	for i, item := range rawAnchors {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if hi >= len(handles) {
			break
		}
		if strings.TrimSpace(handles[hi]) == "" {
			continue
		}
		m["handle"] = handles[hi]
		if pack, ok := obj["pack"].(map[string]any); ok {
			if windows, ok := pack["substance"].([]any); ok {
				for _, item := range windows {
					if window, ok := item.(map[string]any); ok && window["path"] == m["path"] && windowContainsAnchor(window, m) {
						window["handle"] = handles[hi]
					}
				}
			}
		}
		rawAnchors[i] = m
		hi++
	}
	if len(rawAnchors) > 0 {
		obj["anchors"] = rawAnchors
	} else if len(handles) > 0 {
		patchSummarizePackHandles(obj, handles)
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(obj); err != nil {
		return content, err
	}
	return prefix + strings.TrimSuffix(out.String(), "\n") + suffix, nil
}

func patchSummarizePackHandles(obj map[string]any, handles []string) {
	hi := 0
	if pack, ok := obj["pack"].(map[string]any); ok {
		if callSites, ok := pack["call_sites"].([]any); ok {
			for i, item := range callSites {
				m, ok := item.(map[string]any)
				if !ok || hi >= len(handles) {
					continue
				}
				if strings.TrimSpace(handles[hi]) != "" {
					m["handle"] = handles[hi]
					callSites[i] = m
					hi++
				}
			}
			pack["call_sites"] = callSites
		}
		if skeleton, ok := pack["skeleton"].([]any); ok {
			for i, item := range skeleton {
				m, ok := item.(map[string]any)
				if !ok || hi >= len(handles) {
					continue
				}
				kind, _ := m["kind"].(string)
				name, _ := m["name"].(string)
				line, _ := m["line"].(float64)
				if !summarizeSkeletonCitable(summarizePackSymbolWire{Kind: kind, Name: name, Line: int(line)}) {
					continue
				}
				if strings.TrimSpace(handles[hi]) != "" {
					m["handle"] = handles[hi]
					skeleton[i] = m
					hi++
				}
			}
			pack["skeleton"] = skeleton
		}
	}
	if hi == 0 && len(handles) > 0 {
		obj["highlight_handles"] = handles
	}
}

// ExtractEmbeddedHandles returns child evidence handles stamped inside a host-enveloped tool JSON payload.
func ExtractEmbeddedHandles(content string, parentHandles ...string) []string {
	prefix, body, _, ok := hostmarker.SplitToolJSONBody(content)
	if !ok {
		return nil
	}
	parentHandle := leadingEnvelopeHandle(prefix)
	if parentHandle == "" {
		return nil
	}
	if len(parentHandles) > 0 && !containsHandle(parentHandles, parentHandle) {
		return nil
	}
	parentKind, parentOrd := ParseHandleOrdinal(parentHandle)
	if parentOrd <= 0 {
		return nil
	}
	parentScope := handleScopePrefix(parentHandle)
	obj, ok := jsonToolPayload(body)
	if !ok {
		return nil
	}
	var out []string
	seen := make(map[string]struct{})
	add := func(raw any) {
		h, _ := raw.(string)
		h = strings.TrimSpace(h)
		if !validEmbeddedChildHandle(parentKind, parentOrd, parentScope, h) {
			return
		}
		if _, ok := seen[h]; ok {
			return
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	addItems := func(raw any, field string) {
		items, ok := raw.([]any)
		if !ok {
			return
		}
		for _, item := range items {
			switch v := item.(type) {
			case map[string]any:
				add(v[field])
			case string:
				if field == "" {
					add(v)
				}
			}
		}
	}
	switch parentKind {
	case "summarize":
		addItems(obj["anchors"], "handle")
		addItems(obj["highlight_handles"], "")
		if pack, ok := obj["pack"].(map[string]any); ok {
			addItems(pack["substance"], "handle")
			addItems(pack["call_sites"], "handle")
			addItems(pack["skeleton"], "handle")
		}
	case "page":
		addItems(obj["frames"], "evidence_handle")
	case "read", "list", "grep", "find":
		addItems(obj["highlights"], "handle")
	}
	return out
}

func leadingEnvelopeHandle(prefix string) string {
	rest := strings.TrimSpace(prefix)
	if strings.HasPrefix(rest, hostmarker.CompactionBannerOpen) {
		end := strings.Index(rest, hostmarker.CompactionBannerClose)
		if end < 0 {
			return ""
		}
		rest = strings.TrimSpace(rest[end+len(hostmarker.CompactionBannerClose):])
		if strings.HasPrefix(rest, hostmarker.VerbatimHeadTail) {
			rest = strings.TrimSpace(strings.TrimPrefix(rest, hostmarker.VerbatimHeadTail))
		}
	}
	var last string
	for {
		tag, after := hostmarker.SplitEvidenceHandleTag(rest)
		if tag == "" {
			break
		}
		last = strings.Trim(strings.TrimSpace(tag), "[]")
		rest = strings.TrimSpace(after)
	}
	return last
}

func containsHandle(handles []string, want string) bool {
	for _, h := range handles {
		if strings.TrimSpace(h) == want {
			return true
		}
	}
	return false
}

func handleScopePrefix(handle string) string {
	handle = strings.TrimSpace(handle)
	hash := strings.IndexByte(handle, '#')
	if hash < 0 {
		return ""
	}
	if sep := strings.LastIndexByte(handle[:hash], ':'); sep >= 0 {
		return handle[:sep+1]
	}
	return ""
}

func validEmbeddedChildHandle(parentKind string, parentOrd int, parentScope, child string) bool {
	if child == "" || handleScopePrefix(child) != parentScope {
		return false
	}
	childKind, childOrd := ParseHandleOrdinal(child)
	if childKind != parentKind || childOrd <= 0 {
		return false
	}
	switch parentKind {
	case "summarize", "page":
		return childOrd < parentOrd
	case "read", "list", "grep", "find":
		return childOrd > parentOrd
	default:
		return false
	}
}

// splitToolPayloadEnvelope preserves markers around a patched JSON body.
func splitToolPayloadEnvelope(content string) (prefix, body, suffix string) {
	if p, b, s, ok := hostmarker.SplitToolJSONBody(content); ok {
		return p, b, s
	}
	return "", content, ""
}

func windowContainsAnchor(window, anchor map[string]any) bool {
	start, ok := window["start_line"].(float64)
	if !ok {
		return false
	}
	end, ok := window["end_line"].(float64)
	if !ok {
		return false
	}
	line, ok := anchor["line"].(float64)
	return ok && line >= start && line <= end
}
