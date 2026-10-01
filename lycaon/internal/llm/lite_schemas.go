package llm

import (
	"encoding/json"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

// Lite JSON Schemas for utility Completes. These constrain
// freeform content without tool calling. Host parse remains resilient when a
// provider ignores or rejects response_format.

var (
	curateSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["selections", "gloss"],
  "properties": {
    "selections": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["path", "line", "excerpt"],
        "properties": {
          "path": { "type": "string" },
          "line": { "type": "integer" },
          "excerpt": { "type": "string" }
        }
      }
    },
    "gloss": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["label"],
        "properties": {
          "label": { "type": "string" }
        }
      }
    }
  }
}`)
)

// CurateResponseFormat constrains host synthesis-curate selection JSON.
func CurateResponseFormat() *modelcall.ResponseFormat {
	return modelcall.JSONSchemaFormat("curate", curateSchema)
}
