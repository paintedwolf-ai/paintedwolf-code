package parse

import (
	"context"
	"encoding/json"
	"testing"
)

// FuzzExtractJSON checks that the JSON-fence extractor never panics on arbitrary input.
func FuzzExtractJSON(f *testing.F) {
	seeds := []string{
		"",
		"   ",
		"{}",
		"```json\n{\"k\":1}\n```",
		"```\n{\"k\":[1,2,3]}\n```",
		"not json at all",
		"```json\nnot json\n```",
		"prefix\n```json\n{}\n```\nsuffix",
		"```",
		"``````",
		"```json\n```",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	svc := NewDefaultService()
	f.Fuzz(func(t *testing.T, raw string) {
		_, _ = svc.ExtractJSON(context.Background(), raw, nil)
	})
}

// FuzzValidate checks that the schema validator never panics on arbitrary
// payloads against a small fixed schema.
func FuzzValidate(f *testing.F) {
	schema := json.RawMessage(`{"type":"object","properties":{"k":{"type":"string"}}}`)
	seeds := []string{
		"",
		"{}",
		"{\"k\":\"v\"}",
		"{\"k\":1}",
		"[1,2,3]",
		"null",
		"{\"k\":\"v\",\"extra\":{\"nested\":true}}",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	svc := NewDefaultService()
	f.Fuzz(func(t *testing.T, raw string) {
		_, _ = svc.Validate(context.Background(), raw, schema)
	})
}
