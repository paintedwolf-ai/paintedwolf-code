package transcript

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/pkg/api"
)

// projectSourceText separates retained source bytes from their result coordinates.
// Command output is delivered as printed and never comes through here.
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
	case "recall":
		return projectRecallText(message, part, parts)
	case "source_history":
		return projectSourceHistoryVersion(message, part, parts)
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

func receiptMetadata(message api.Message, part api.MessageContentPart, tool string) (handles []string, metadata map[string]json.RawMessage, mode, suffix string, ok bool) {
	handles, body := resultHandles(message, part.Content)
	prefix, body, suffix, ok := hostmarker.SplitToolJSONBody(body)
	if !ok || prefix != "" {
		return nil, nil, "", "", false
	}
	if json.Unmarshal([]byte(body), &metadata) != nil {
		return nil, nil, "", "", false
	}
	var receipt struct {
		Tool string `json:"tool"`
	}
	if json.Unmarshal(metadata["receipt"], &receipt) != nil || receipt.Tool != tool {
		return nil, nil, "", "", false
	}
	if json.Unmarshal(metadata["mode"], &mode) != nil {
		return nil, nil, "", "", false
	}
	return handles, metadata, mode, suffix, true
}

// projectSourceHistoryVersion presents a retained version's lines like a read,
// labeled with the version they came from.
func projectSourceHistoryVersion(message api.Message, part api.MessageContentPart, parts []api.MessageContentPart) []api.MessageContentPart {
	handles, metadata, mode, suffix, ok := receiptMetadata(message, part, "source_history")
	if !ok || mode != "version" {
		return parts
	}
	text, ok := numberedSourceBlock(metadata, "content")
	if !ok {
		return parts
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return parts
	}
	_ = json.Unmarshal(metadata["version_id"], &text.version)
	text.handle = strings.Join(handles, " ")
	return sourceParts(part, "source_history_metadata", handles, encoded, []sourceText{text}, suffix)
}

type sourceText struct {
	text, path, version, handle string
	start, end                  int
}

func (s sourceText) label() string {
	label := "File contents"
	if s.path != "" {
		label += fmt.Sprintf(" %q", s.path)
	}
	if s.version != "" {
		label += fmt.Sprintf(" at version %q", s.version)
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
