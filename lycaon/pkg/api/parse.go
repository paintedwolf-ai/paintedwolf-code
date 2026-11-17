package api

import "encoding/json"

// ParseResult is the outcome of parse validation.
type ParseResult struct {
	Valid  bool            `json:"valid"`
	Errors []string        `json:"errors,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// ExtractResult is JSON extracted from markdown fences.
type ExtractResult struct {
	JSON        json.RawMessage `json:"json,omitempty"`
	Extracted   bool            `json:"extracted"`
	SourceFence string          `json:"source_fence,omitempty"`
}
