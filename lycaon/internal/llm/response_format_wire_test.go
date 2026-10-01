package llm

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEncodeResponseFormatJSONSchema(t *testing.T) {
	provider := openaicompat.New("openai", "https://api.openai.com/v1", "key", []modelinfo.Entry{
		{ID: "gpt-4.1-mini"},
	})
	req := modelcall.CompletionRequest{
		Model:          "gpt-4.1-mini",
		Messages:       []api.Message{{Role: api.MessageRoleUser, Content: "summarize"}},
		ResponseFormat: CurateResponseFormat(),
	}
	body, err := provider.Prepare(req, false)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rf, ok := wire["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("missing response_format: %s", body)
	}
	if rf["type"] != "json_schema" {
		t.Fatalf("type = %v want json_schema", rf["type"])
	}
	schemaObj, ok := rf["json_schema"].(map[string]any)
	if !ok {
		t.Fatalf("missing json_schema: %s", body)
	}
	if schemaObj["name"] != "curate" {
		t.Fatalf("name = %v", schemaObj["name"])
	}
	if schemaObj["strict"] != true {
		t.Fatalf("strict = %v want true", schemaObj["strict"])
	}
	schema, ok := schemaObj["schema"].(map[string]any)
	if !ok {
		t.Fatalf("schema missing: %s", body)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok || props["selections"] == nil {
		t.Fatalf("schema properties incomplete: %s", body)
	}
}
