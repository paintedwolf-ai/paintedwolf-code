package transcript

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// projectSourceText separates source bytes from their read-result coordinates.
func projectSourceText(message api.Message, parts []api.MessageContentPart) []api.MessageContentPart {
	if message.Role != api.MessageRoleTool || message.Origin != api.MessageOriginTool || message.ToolResult == nil ||
		message.ToolResult.Tool != "read" || message.ToolResult.Outcome != api.ToolResultOutcomeCompleted || len(parts) != 1 {
		return parts
	}
	part := parts[0]
	if part.Origin != api.MessageOriginTool || part.Authority != api.ContentAuthorityNone {
		return parts
	}
	body := part.Content
	var handles []string
	for _, handle := range message.EvidenceHandles {
		prefix := "[" + handle + "]\n"
		if strings.HasPrefix(body, prefix) {
			handles = append(handles, "["+handle+"]")
			body = strings.TrimPrefix(body, prefix)
		}
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
	var sources []string
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
		encoded, _ := json.Marshal(ranges)
		metadata["ranges"] = encoded
	default:
		return parts
	}
	encoded, _ := json.Marshal(metadata)
	out := make([]api.MessageContentPart, 0, len(sources)+1)
	part.Source = "read_metadata"
	part.Content = strings.Join(append(handles, string(encoded)), "\n")
	out = append(out, part)
	for _, source := range sources {
		fence := sourceFence(source)
		part.Source = "source"
		part.Content = fence + "\n" + source + "\n" + fence
		out = append(out, part)
	}
	return out
}

func readSourceBlock(block map[string]json.RawMessage) (string, bool) {
	var content string
	var start, end int
	if json.Unmarshal(block["content"], &content) != nil || json.Unmarshal(block["end_line"], &end) != nil {
		return "", false
	}
	if json.Unmarshal(block["offset"], &start) != nil || start <= 0 {
		if json.Unmarshal(block["start_line"], &start) != nil {
			return "", false
		}
	}
	if start <= 0 || end < start {
		return "", false
	}
	lines := strings.Split(content, "\n")
	if end-start+1 != len(lines) {
		return "", false
	}
	for i, line := range lines {
		prefix := fmt.Sprintf("%6d: ", start+i)
		if !strings.HasPrefix(line, prefix) {
			return "", false
		}
		lines[i] = strings.TrimPrefix(line, prefix)
	}
	delete(block, "content")
	return strings.Join(lines, "\n"), true
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
