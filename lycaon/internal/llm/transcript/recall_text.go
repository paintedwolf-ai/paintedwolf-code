package transcript

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/pkg/api"
)

// projectRecallText keeps recorded bytes outside the recovery metadata envelope.
func projectRecallText(message api.Message, part api.MessageContentPart, parts []api.MessageContentPart) []api.MessageContentPart {
	handles, body := resultHandles(message, part.Content)
	prefix, body, suffix, ok := hostmarker.SplitToolJSONBody(body)
	if !ok || prefix != "" {
		return parts
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &metadata) != nil {
		return parts
	}
	var sources []string
	if raw, exists := metadata["history"]; exists {
		var history map[string]json.RawMessage
		if json.Unmarshal(raw, &history) != nil {
			return parts
		}
		var format string
		var recorded bool
		if json.Unmarshal(history["format"], &format) != nil || (format != "text" && format != "record") || json.Unmarshal(history["recorded"], &recorded) != nil || !recorded {
			return parts
		}
		var entries []map[string]json.RawMessage
		if json.Unmarshal(history["entries"], &entries) != nil || len(entries) == 0 {
			return parts
		}
		for _, entry := range entries {
			var text string
			if json.Unmarshal(entry["content"], &text) != nil {
				return parts
			}
			sources = append(sources, text)
			entry["body_part"] = json.RawMessage(strconv.Itoa(len(sources)))
			delete(entry, "content")
		}
		encoded, err := json.Marshal(entries)
		if err != nil {
			return parts
		}
		history["entries"] = encoded
		if encoded, err = json.Marshal(history); err != nil {
			return parts
		}
		metadata["history"] = encoded
	} else {
		var hits []map[string]json.RawMessage
		if json.Unmarshal(metadata["hits"], &hits) != nil {
			return parts
		}
		for _, hit := range hits {
			raw, exists := hit["body"]
			if !exists {
				continue
			}
			var lines []string
			if json.Unmarshal(raw, &lines) != nil {
				return parts
			}
			if len(lines) == 0 {
				continue
			}
			sources = append(sources, strings.Join(lines, "\n"))
			hit["body_part"] = json.RawMessage(strconv.Itoa(len(sources)))
			delete(hit, "body")
		}
		if len(sources) == 0 {
			return parts
		}
		encoded, err := json.Marshal(hits)
		if err != nil {
			return parts
		}
		metadata["hits"] = encoded
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return parts
	}
	out := sourceParts(part, "recall_metadata", handles, encoded, sources, suffix)
	for i := 1; i < len(out); i++ {
		if out[i].Source == "source" {
			out[i].Source = "recorded"
		}
	}
	return out
}
