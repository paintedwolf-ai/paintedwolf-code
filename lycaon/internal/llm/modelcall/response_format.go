package modelcall

import (
	"encoding/json"
)

// ResponseFormatType selects structured output.
type ResponseFormatType string

const (
	// ResponseFormatJSONObject asks for a JSON object with no schema.
	ResponseFormatJSONObject ResponseFormatType = "json_object"
	// ResponseFormatJSONSchema asks for JSON matching Schema.
	ResponseFormatJSONSchema ResponseFormatType = "json_schema"
)

// ResponseFormat defines structured completion output.
type ResponseFormat struct {
	Type ResponseFormatType
	// Name is required for json_schema (provider schema id).
	Name string
	// Schema is the JSON Schema document for json_schema.
	Schema json.RawMessage
	// Strict requests schema adherence.
	Strict bool
}

// JSONSchemaFormat builds a json_schema response_format.
func JSONSchemaFormat(name string, schema json.RawMessage) *ResponseFormat {
	return &ResponseFormat{
		Type:   ResponseFormatJSONSchema,
		Name:   name,
		Schema: schema,
		Strict: true,
	}
}

type ResponseFormatWire struct {
	Type       string                    `json:"type"`
	JSONSchema *ResponseFormatSchemaWire `json:"json_schema,omitempty"`
}

type ResponseFormatSchemaWire struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict,omitempty"`
	Schema json.RawMessage `json:"schema"`
}

func ResponseFormatToWire(rf *ResponseFormat) *ResponseFormatWire {
	if rf == nil {
		return nil
	}
	switch rf.Type {
	case ResponseFormatJSONObject:
		return &ResponseFormatWire{Type: "json_object"}
	case ResponseFormatJSONSchema:
		if len(rf.Schema) == 0 || rf.Name == "" {
			return nil
		}
		return &ResponseFormatWire{
			Type: "json_schema",
			JSONSchema: &ResponseFormatSchemaWire{
				Name:   rf.Name,
				Strict: rf.Strict,
				Schema: rf.Schema,
			},
		}
	default:
		return nil
	}
}

func ResponseFormatToOllama(rf *ResponseFormat) json.RawMessage {
	if rf == nil {
		return nil
	}
	switch rf.Type {
	case ResponseFormatJSONObject:
		return json.RawMessage(`"json"`)
	case ResponseFormatJSONSchema:
		if len(rf.Schema) == 0 {
			return nil
		}
		return rf.Schema
	default:
		return nil
	}
}
