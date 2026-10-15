package transcript

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/pkg/api"
)

// projectSourceText separates source bytes from their result coordinates.
func projectSourceText(message api.Message, parts []api.MessageContentPart) []api.MessageContentPart {
	if message.Role != api.MessageRoleTool || message.Origin != api.MessageOriginTool || message.ToolResult == nil ||
		message.ToolResult.Outcome != api.ToolResultOutcomeCompleted || len(parts) != 1 {
		return parts
	}
	part := parts[0]
	if part.Origin != api.MessageOriginTool || part.Authority != api.ContentAuthorityNone {
		return parts
	}
	switch message.ToolResult.Tool {
	case "read":
		return projectReadSource(message, part, parts)
	case "summarize":
		return projectSummarizeSource(message, part, parts)
	}
	return parts
}

// resultHandles splits leading evidence handles from a result body.
func resultHandles(message api.Message, body string) ([]string, string) {
	var handles []string
	for _, handle := range message.EvidenceHandles {
		prefix := "[" + handle + "]\n"
		if strings.HasPrefix(body, prefix) {
			handles = append(handles, "["+handle+"]")
			body = strings.TrimPrefix(body, prefix)
		}
	}
	return handles, body
}

// projectSummarizeSource sends each source window as text beside its coordinates.
func projectSummarizeSource(message api.Message, part api.MessageContentPart, parts []api.MessageContentPart) []api.MessageContentPart {
	handles, body := resultHandles(message, part.Content)
	prefix, body, suffix, ok := hostmarker.SplitToolJSONBody(body)
	if !ok || prefix != "" {
		return parts
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &metadata) != nil {
		return parts
	}
	var pack map[string]json.RawMessage
	if json.Unmarshal(metadata["pack"], &pack) != nil {
		return parts
	}
	var windows []map[string]json.RawMessage
	if json.Unmarshal(pack["substance"], &windows) != nil || len(windows) == 0 {
		return parts
	}
	sources := make([]sourceText, 0, len(windows))
	for _, window := range windows {
		text, ok := numberedSourceBlock(window, "body")
		if !ok {
			return parts
		}
		sources = append(sources, text)
	}
	encoded, err := json.Marshal(windows)
	if err != nil {
		return parts
	}
	pack["substance"] = encoded
	if encoded, err = json.Marshal(pack); err != nil {
		return parts
	}
	metadata["pack"] = encoded
	if encoded, err = json.Marshal(metadata); err != nil {
		return parts
	}
	return sourceParts(part, "summarize_metadata", handles, encoded, sources, suffix)
}

func projectReadSource(message api.Message, part api.MessageContentPart, parts []api.MessageContentPart) []api.MessageContentPart {
	handles, body := resultHandles(message, part.Content)
	prefix, body, suffix, ok := hostmarker.SplitToolJSONBody(body)
	if !ok || prefix != "" {
		return parts
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &metadata) != nil {
		return parts
	}
	var receipt struct {
		Tool string `json:"tool"`
	}
	if json.Unmarshal(metadata["receipt"], &receipt) != nil || receipt.Tool != "read" {
		return parts
	}
	var mode string
	if json.Unmarshal(metadata["mode"], &mode) != nil {
		return parts
	}
	var sources []sourceText
	switch mode {
	case "content", "symbol":
		text, ok := readSourceBlock(metadata)
		if !ok {
			return parts
		}
		sources = append(sources, text)
	case "ranges":
		var ranges []map[string]json.RawMessage
		if json.Unmarshal(metadata["ranges"], &ranges) != nil || len(ranges) == 0 {
			return parts
		}
		for _, block := range ranges {
			text, ok := readSourceBlock(block)
			if !ok {
				return parts
			}
			sources = append(sources, text)
		}
		encoded, err := json.Marshal(ranges)
		if err != nil {
			return parts
		}
		metadata["ranges"] = encoded
	default:
		return parts
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return parts
	}
	var path string
	_ = json.Unmarshal(metadata["path"], &path)
	for i := range sources {
		if sources[i].path == "" {
			sources[i].path = path
		}
		sources[i].handle = strings.Join(handles, " ")
	}
	return sourceParts(part, "read_metadata", handles, encoded, sources, suffix)
}

type sourceText struct {
	text, path, handle string
	start, end         int
}

func (s sourceText) label() string {
	label := "File contents"
	if s.path != "" {
		label += fmt.Sprintf(" %q", s.path)
	}
	label += fmt.Sprintf(" (lines %d–%d)", s.start, s.end)
	if s.handle != "" {
		label += fmt.Sprintf("; evidence %q", s.handle)
	}
	return label + ":"
}

// sourceParts emits result metadata followed by one fenced part per source block, in metadata order.
func sourceParts(part api.MessageContentPart, metadataSource string, handles []string, metadata []byte, sources []sourceText, suffix string) []api.MessageContentPart {
	out := make([]api.MessageContentPart, 0, len(sources)+1)
	part.Source = metadataSource
	part.Content = "Tool result metadata:\n" + strings.Join(append(handles, string(metadata)), "\n")
	out = append(out, part)
	for _, source := range sources {
		fence := sourceFence(source.text)
		part.Source = "source"
		part.Content = source.label() + "\n" + fence + "\n" + source.text + "\n" + fence
		out = append(out, part)
	}
	if strings.TrimSpace(suffix) != "" {
		part.Source = "result_suffix"
		part.Content = suffix
		out = append(out, part)
	}
	return out
}

func readSourceBlock(block map[string]json.RawMessage) (sourceText, bool) {
	return numberedSourceBlock(block, "content")
}

// numberedSourceBlock decodes the host's numbered-source format held under key and removes it from block.
func numberedSourceBlock(block map[string]json.RawMessage, key string) (sourceText, bool) {
	var content string
	var start, end int
	if json.Unmarshal(block[key], &content) != nil || json.Unmarshal(block["end_line"], &end) != nil {
		return sourceText{}, false
	}
	if json.Unmarshal(block["offset"], &start) != nil || start <= 0 {
		if json.Unmarshal(block["start_line"], &start) != nil {
			return sourceText{}, false
		}
	}
	if start <= 0 || end < start {
		return sourceText{}, false
	}
	lines := strings.Split(content, "\n")
	if end-start+1 != len(lines) {
		return sourceText{}, false
	}
	for i, line := range lines {
		prefix := fmt.Sprintf("%6d: ", start+i)
		if !strings.HasPrefix(line, prefix) {
			return sourceText{}, false
		}
		lines[i] = strings.TrimPrefix(line, prefix)
	}
	delete(block, key)
	result := sourceText{text: strings.Join(lines, "\n"), start: start, end: end}
	_ = json.Unmarshal(block["path"], &result.path)
	_ = json.Unmarshal(block["handle"], &result.handle)
	return result, true
}

func sourceFence(source string) string {
	var ticks, tildes, tickRun, tildeRun int
	for _, char := range source {
		if char == '`' {
			tickRun++
			ticks = max(ticks, tickRun)
		} else {
			tickRun = 0
		}
		if char == '~' {
			tildeRun++
			tildes = max(tildes, tildeRun)
		} else {
			tildeRun = 0
		}
	}
	if ticks <= tildes {
		return strings.Repeat("`", max(3, ticks+1))
	}
	return strings.Repeat("~", max(3, tildes+1))
}
