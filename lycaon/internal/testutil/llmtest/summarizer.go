package llmtest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const defaultSummary = "Continue coordinator work from compacted context."

// Summarizer returns fixed text or continuation JSON using the supplied format reader.
type Summarizer struct {
	Text string
	Err  error
	// Structured reads the caller's summary-format marker.
	Structured func(context.Context) bool
}

func (s Summarizer) Summarize(ctx context.Context, _, _ string, _ int) (string, error) {
	if s.Err != nil {
		return "", s.Err
	}
	if s.Structured != nil && s.Structured(ctx) {
		return SummaryJSON(s.Text)
	}
	if s.Text != "" {
		return s.Text, nil
	}
	return defaultSummary, nil
}

// SummaryJSON supplies every list required by the continuation response schema.
func SummaryJSON(text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		text = defaultSummary
	}
	value := struct {
		Facts       []string `json:"facts"`
		Completed   []string `json:"completed"`
		Pending     []string `json:"pending"`
		Reacquire   []string `json:"reacquire"`
		Constraints []string `json:"constraints"`
	}{Facts: []string{text}, Completed: []string{}, Pending: []string{}, Reacquire: []string{}, Constraints: []string{}}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode continuation fixture: %w", err)
	}
	return string(raw), nil
}
