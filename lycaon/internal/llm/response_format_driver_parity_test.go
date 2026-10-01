package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	anthropicprovider "github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	ollamaprovider "github.com/lycaon/lycaon/internal/llm/providers/ollama"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	vertexexpressprovider "github.com/lycaon/lycaon/internal/llm/providers/vertexexpress"
	"github.com/lycaon/lycaon/pkg/api"
)

// responseFormatProjection is how one driver dialect can express a host
// ResponseFormat on its own wire.
type responseFormatProjection int

const (
	// projectsSchema carries the JSON Schema document itself, so the AI provider
	// constrains decoding to it.
	projectsSchema responseFormatProjection = iota
	// projectsJSONOnly can only ask for "some JSON" — the schema dialect does not
	// survive, so the host parse stays the real guarantee.
	projectsJSONOnly
	// cannotProject has no request-level structured-output field at all.
	// Structured output on these dialects goes through tool calling instead.
	cannotProject
)

// Each request dialect must explicitly project or omit the host response format.
func TestResponseFormatDriverParity(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["a"],` +
		`"properties":{"a":{"type":"array","items":{"type":"string"}}}}`)
	format := modelcall.JSONSchemaFormat("parity", schema)

	cases := []struct {
		driver string
		want   responseFormatProjection
		// encode returns the driver's serialized request body, plus the
		// dialect-specific field the projection is expected to land in.
		encode func(t *testing.T, rf *modelcall.ResponseFormat) (body string, field string)
	}{
		{
			driver: "openai-compat",
			want:   projectsSchema,
			encode: func(t *testing.T, rf *modelcall.ResponseFormat) (string, string) {
				p := openaicompat.New("openai", "https://api.openai.com/v1", "key",
					[]modelinfo.Entry{{ID: "gpt-4.1-mini"}})
				raw, err := p.Prepare(modelcall.CompletionRequest{
					Model:          "gpt-4.1-mini",
					Messages:       []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
					ResponseFormat: rf,
				}, false)
				if err != nil {
					t.Fatalf("openai encode: %v", err)
				}
				return string(raw), "response_format"
			},
		},
		{
			driver: "ollama",
			want:   projectsSchema,
			encode: func(t *testing.T, rf *modelcall.ResponseFormat) (string, string) {
				p := ollamaprovider.New("ollama", "http://127.0.0.1:11434", "",
					[]modelinfo.Entry{{ID: "small", ContextLength: 8192}})
				built, _, err := p.Prepare(t.Context(), modelcall.CompletionRequest{
					Model:          "small",
					Messages:       []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
					ResponseFormat: rf,
				}, false)
				if err != nil {
					t.Fatalf("ollama buildRequest: %v", err)
				}
				raw, err := json.Marshal(built)
				if err != nil {
					t.Fatalf("ollama marshal: %v", err)
				}
				return string(raw), "format"
			},
		},
		{
			driver: "vertex-express",
			want:   projectsJSONOnly,
			encode: func(t *testing.T, rf *modelcall.ResponseFormat) (string, string) {
				p := vertexexpressprovider.New("vertex", "https://example.invalid", "key",
					[]modelinfo.Entry{{ID: "gemini-2.5-flash"}})
				raw, err := json.Marshal(p.Prepare(modelcall.CompletionRequest{
					Model:          "gemini-2.5-flash",
					Messages:       []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
					ResponseFormat: rf,
				}))
				if err != nil {
					t.Fatalf("vertex marshal: %v", err)
				}
				return string(raw), "responseMimeType"
			},
		},
		{
			driver: "anthropic",
			want:   cannotProject,
			encode: func(t *testing.T, rf *modelcall.ResponseFormat) (string, string) {
				p := anthropicprovider.New("anthropic", "https://api.anthropic.com", "key",
					[]modelinfo.Entry{{ID: "claude-sonnet-5"}})
				raw, err := json.Marshal(p.Prepare(modelcall.CompletionRequest{
					Model:          "claude-sonnet-5",
					Messages:       []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
					ResponseFormat: rf,
				}, false))
				if err != nil {
					t.Fatalf("anthropic marshal: %v", err)
				}
				return string(raw), ""
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.driver, func(t *testing.T) {
			body, field := tc.encode(t, format)

			switch tc.want {
			case projectsSchema:
				if !strings.Contains(body, `"`+field+`"`) {
					t.Fatalf("%s must carry ResponseFormat in %q: %s", tc.driver, field, body)
				}
				// The schema document itself has to survive, not just the envelope:
				// a driver that sends an empty or renamed constraint constrains nothing.
				if !strings.Contains(body, `"required"`) || !strings.Contains(body, `"items"`) {
					t.Fatalf("%s dropped the schema document from %q: %s", tc.driver, field, body)
				}
			case projectsJSONOnly:
				if !strings.Contains(body, `"`+field+`"`) {
					t.Fatalf("%s must request JSON via %q: %s", tc.driver, field, body)
				}
			case cannotProject:
				// The row goes stale if this dialect gains a structured-output field.
				if strings.Contains(body, `"response_format"`) || strings.Contains(body, `"format"`) {
					t.Fatalf("%s now has a structured-output field — update the parity table: %s",
						tc.driver, body)
				}
			}

			// A nil ResponseFormat must not leave a constraint behind on any dialect.
			plain, plainField := tc.encode(t, nil)
			if plainField != "" && strings.Contains(plain, `"`+plainField+`"`) {
				t.Fatalf("%s must omit %q when ResponseFormat is nil: %s",
					tc.driver, plainField, plain)
			}
		})
	}
}

// json_object asks Ollama for unconstrained JSON via the literal string form of
// `format`, which is a different wire shape than the schema document.
func TestResponseFormatToOllamaJSONObject(t *testing.T) {
	got := modelcall.ResponseFormatToOllama(&modelcall.ResponseFormat{Type: modelcall.ResponseFormatJSONObject})
	if string(got) != `"json"` {
		t.Fatalf("json_object format = %s, want \"json\"", got)
	}
	if modelcall.ResponseFormatToOllama(nil) != nil {
		t.Fatalf("nil ResponseFormat must project nothing")
	}
	if modelcall.ResponseFormatToOllama(&modelcall.ResponseFormat{Type: modelcall.ResponseFormatJSONSchema}) != nil {
		t.Fatalf("json_schema with no schema document must project nothing")
	}
}
