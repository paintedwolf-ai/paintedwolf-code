package providerwire

import (
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/tools"
)

func ProjectToolMetaForModel(meta tools.ToolMeta) tools.ToolMeta {
	if !meta.UntrustedMetadata {
		return meta
	}
	meta.Description = transcript.LabelToolMetadata(meta.Description)
	meta.ArgsSchema = markToolSchemaAnnotations(meta.ArgsSchema)
	return meta
}

func markToolSchemaAnnotations(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	out := make(map[string]any, len(schema))
	for key, value := range schema {
		switch key {
		case "description", "title", "$comment":
			if text, ok := value.(string); ok {
				out[key] = transcript.LabelToolMetadata(text)
				continue
			}
		case "examples":
			// An example is a value the schema accepts; a header would change it.
			out[key] = value
			continue
		}
		out[key] = cloneAndMarkToolSchema(value)
	}
	return out
}

func cloneAndMarkToolSchema(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return markToolSchemaAnnotations(typed)
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = cloneAndMarkToolSchema(typed[i])
		}
		return out
	default:
		return value
	}
}
